package relational

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"M365Copilot2ApiX/backend/internal/domain/account"
	"M365Copilot2ApiX/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	accountLinkAdvisoryNamespace int32 = 0x47524F4B // GROK
	accountLinkAdvisoryOperation int32 = 0x4C4E4B44 // LNKD
	accountLinkLockTimeout             = 5 * time.Second
)

// lockAccountLinkMutation serializes link-table writes and account deletions on PostgreSQL.
// These maintenance operations are rare, and a shared transaction-scoped lock prevents
// link snapshot races and cyclic account/FK row-lock acquisition across mutation paths.
func lockAccountLinkMutation(tx *gorm.DB) error {
	return lockAccountLinkMutationWithTimeout(tx, accountLinkLockTimeout)
}

func lockAccountLinkMutationWithTimeout(tx *gorm.DB, timeout time.Duration) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	if timeout <= 0 {
		timeout = accountLinkLockTimeout
	}
	parentCtx := tx.Statement.Context
	lockCtx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()
	err := tx.WithContext(lockCtx).Exec("SELECT pg_advisory_xact_lock(?, ?)", accountLinkAdvisoryNamespace, accountLinkAdvisoryOperation).Error
	if err != nil && parentCtx.Err() != nil {
		return parentCtx.Err()
	}
	if err != nil && errors.Is(lockCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: 账号关联关系正在变更，请稍后重试", repository.ErrConflict)
	}
	return err
}

func (r *AccountRepository) UpdateIdentityMetadata(ctx context.Context, accountID uint64, email, userID, teamID string) error {
	if accountID == 0 {
		return repository.ErrNotFound
	}
	updates := make(map[string]any, 3)
	if email = strings.TrimSpace(email); email != "" {
		updates["email"] = email
	}
	if userID = strings.TrimSpace(userID); userID != "" {
		updates["user_id"] = userID
	}
	if teamID = strings.TrimSpace(teamID); teamID != "" {
		updates["team_id"] = teamID
	}
	if len(updates) == 0 {
		return nil
	}
	result := r.db.db.WithContext(ctx).Model(&accountModel{}).Where("id = ?", accountID).Updates(updates)
	if result.Error != nil {
		return mapError(result.Error)
	}
	if result.RowsAffected == 0 {
		return repository.ErrNotFound
	}
	r.notifyInvalidation(ctx, repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged, AccountID: accountID})
	return nil
}

// ReconcileProviderLinks is a no-op for M365; there are no cross-provider links.
func (r *AccountRepository) ReconcileProviderLinks(ctx context.Context, accountID uint64) error {
	if accountID == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// ResolveLinkedDeleteIDs returns root IDs without expansion. M365 has a single
// provider with no linked peers.
func (r *AccountRepository) ResolveLinkedDeleteIDs(ctx context.Context, providerValue account.Provider, rootIDs []uint64, targets []account.Provider) (repository.LinkedDeleteResolution, error) {
	return resolveLinkedDeleteIDs(r.db.db.WithContext(ctx), providerValue, rootIDs, targets)
}

func resolveLinkedDeleteIDs(db *gorm.DB, providerValue account.Provider, rootIDs []uint64, targets []account.Provider) (repository.LinkedDeleteResolution, error) {
	result := repository.LinkedDeleteResolution{
		LinkedByProvider: map[account.Provider]int{},
		RootGroups:       map[uint64][]uint64{},
		PeerProviders:    map[uint64]account.Provider{},
	}
	if !providerValue.IsValid() {
		return result, fmt.Errorf("账号来源无效")
	}
	roots := uniqueSortedIDs(rootIDs)
	result.RootIDs = append([]uint64(nil), roots...)
	if len(roots) == 0 {
		result.FinalIDs = nil
		return result, nil
	}
	targetSet := make(map[account.Provider]struct{}, len(targets))
	for _, target := range targets {
		if !target.IsValid() {
			return result, fmt.Errorf("关联删除目标无效")
		}
		if target == providerValue {
			return result, fmt.Errorf("关联删除目标不能包含当前号池")
		}
		targetSet[target] = struct{}{}
	}
	if len(targetSet) == 0 {
		result.FinalIDs = append([]uint64(nil), roots...)
		return result, nil
	}
	// M365 has no linked providers; targets are invalid.
	result.FinalIDs = append([]uint64(nil), roots...)
	return result, nil
}

// deleteLinkedGroupsTx deletes root accounts. M365 has no linked peers.
func deleteLinkedGroupsTx(tx *gorm.DB, providerValue account.Provider, lockedRoots []uint64, targets []account.Provider, skipMedia bool) (repository.LinkedDeleteOutcome, error) {
	outcome := repository.LinkedDeleteOutcome{
		LinkedDeletedByProvider: map[account.Provider]int64{},
	}

	var resolution repository.LinkedDeleteResolution
	var err error
	if len(targets) == 0 {
		resolution = repository.LinkedDeleteResolution{
			RootIDs:          append([]uint64(nil), lockedRoots...),
			FinalIDs:         append([]uint64(nil), lockedRoots...),
			LinkedByProvider: map[account.Provider]int{},
			RootGroups:       map[uint64][]uint64{},
			PeerProviders:    map[uint64]account.Provider{},
		}
	} else {
		if !providerValue.IsValid() {
			return outcome, fmt.Errorf("账号来源无效")
		}
		resolution, err = resolveLinkedDeleteIDs(tx, providerValue, lockedRoots, targets)
		if err != nil {
			return outcome, err
		}
	}
	outcome.Resolution = resolution
	if len(resolution.FinalIDs) == 0 {
		return outcome, nil
	}

	// Lock roots and linked peers in the final set.
	var lockedFinal []uint64
	if err := tx.Model(&accountModel{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", resolution.FinalIDs).Order("id ASC").Pluck("id", &lockedFinal).Error; err != nil {
		return outcome, err
	}
	if len(lockedFinal) == 0 {
		return outcome, nil
	}
	outcome.Resolution.FinalIDs = append([]uint64(nil), lockedFinal...)

	// Apply active-media protection.
	deletable := lockedFinal
	if skipMedia {
		var blocked []uint64
		if err := tx.Model(&mediaJobModel{}).
			Where("account_id IN ? AND status IN ?", lockedFinal, activeMediaJobStatuses()).
			Pluck("account_id", &blocked).Error; err != nil {
			return outcome, err
		}
		if len(blocked) > 0 {
			peerRoot := make(map[uint64]uint64, len(resolution.PeerProviders))
			for root, peers := range resolution.RootGroups {
				for _, peer := range peers {
					peerRoot[peer] = root
				}
			}
			skippedRootSet := map[uint64]struct{}{}
			for _, id := range blocked {
				if root, ok := peerRoot[id]; ok {
					skippedRootSet[root] = struct{}{}
				} else {
					skippedRootSet[id] = struct{}{}
				}
			}
			removed := map[uint64]struct{}{}
			for root := range skippedRootSet {
				removed[root] = struct{}{}
				for _, peer := range resolution.RootGroups[root] {
					removed[peer] = struct{}{}
				}
				outcome.SkippedRoots = append(outcome.SkippedRoots, root)
			}
			sort.Slice(outcome.SkippedRoots, func(i, j int) bool { return outcome.SkippedRoots[i] < outcome.SkippedRoots[j] })
			filtered := make([]uint64, 0, len(lockedFinal))
			for _, id := range lockedFinal {
				if _, skip := removed[id]; !skip {
					filtered = append(filtered, id)
				}
			}
			deletable = filtered
		}
	} else {
		if err := rejectAccountsWithMediaJobs(tx, lockedFinal); err != nil {
			return outcome, err
		}
	}
	if len(deletable) == 0 {
		return outcome, nil
	}

	// Delete remaining rows and count them by provider.
	result := tx.Where("id IN ?", deletable).Delete(&accountModel{})
	if result.Error != nil {
		return outcome, result.Error
	}
	outcome.Deleted = result.RowsAffected
	outcome.DeletedIDs = append([]uint64(nil), deletable...)
	rootSet := make(map[uint64]struct{}, len(resolution.RootIDs))
	for _, id := range resolution.RootIDs {
		rootSet[id] = struct{}{}
	}
	for _, id := range deletable {
		if _, isRoot := rootSet[id]; isRoot {
			outcome.RootsDeleted++
		} else if provider, ok := resolution.PeerProviders[id]; ok {
			outcome.LinkedDeletedByProvider[provider]++
		}
	}
	return outcome, nil
}

// DeleteManyWithLinked locks existing roots, expands linked peers (none for M365),
// re-locks the final set, handles media jobs, and deletes everything in one transaction.
func (r *AccountRepository) DeleteManyWithLinked(ctx context.Context, providerValue account.Provider, rootIDs []uint64, targets []account.Provider, skipMedia bool) (repository.LinkedDeleteOutcome, error) {
	outcome := repository.LinkedDeleteOutcome{
		Resolution:              repository.LinkedDeleteResolution{LinkedByProvider: map[account.Provider]int{}},
		LinkedDeletedByProvider: map[account.Provider]int64{},
	}
	roots := uniqueSortedIDs(rootIDs)
	if len(roots) == 0 {
		return outcome, nil
	}

	err := r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccountLinkMutation(tx); err != nil {
			return err
		}

		var lockedRows []struct {
			ID       uint64
			Provider string
		}
		if err := tx.Model(&accountModel{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "provider").Where("id IN ?", roots).Order("id ASC").Find(&lockedRows).Error; err != nil {
			return err
		}
		if len(lockedRows) == 0 {
			return nil
		}
		lockedRoots := make([]uint64, 0, len(lockedRows))
		for _, row := range lockedRows {
			if providerValue.IsValid() && account.Provider(row.Provider) != providerValue {
				return fmt.Errorf("%w: 账号不属于指定号池", repository.ErrConflict)
			}
			lockedRoots = append(lockedRoots, row.ID)
		}
		inner, err := deleteLinkedGroupsTx(tx, providerValue, lockedRoots, targets, skipMedia)
		if err != nil {
			return err
		}
		outcome = inner
		return nil
	})
	if err != nil {
		return repository.LinkedDeleteOutcome{}, err
	}
	if outcome.Deleted > 0 {
		r.notifyInvalidation(ctx, repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged})
	}
	return outcome, nil
}

// DeleteAccountStatusBatchWithLinked selects at most limit roots by state and ID cursor,
// expands links (none for M365), skips active-media groups, and returns the candidate count and next cursor.
func (r *AccountRepository) DeleteAccountStatusBatchWithLinked(ctx context.Context, providerValue account.Provider, status string, now time.Time, afterID uint64, limit int, targets []account.Provider) (repository.LinkedDeleteOutcome, int, uint64, error) {
	outcome := repository.LinkedDeleteOutcome{
		Resolution:              repository.LinkedDeleteResolution{LinkedByProvider: map[account.Provider]int{}},
		LinkedDeletedByProvider: map[account.Provider]int64{},
	}
	if limit < 1 {
		return outcome, 0, afterID, nil
	}
	if status != "disabled" && status != "reauthRequired" && status != "cooldown" {
		return outcome, 0, afterID, fmt.Errorf("不支持清理账号状态 %q", status)
	}
	candidateCount := 0
	maxCandidateID := afterID
	err := r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccountLinkMutation(tx); err != nil {
			return err
		}
		selection := applyAccountStatusFilter(
			tx.Model(&accountModel{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("provider = ?", providerValue),
			status, now,
		)
		if afterID > 0 {
			selection = selection.Where("id > ?", afterID)
		}
		var candidates []uint64
		if err := selection.Order("id ASC").Limit(limit).Pluck("id", &candidates).Error; err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		candidateCount = len(candidates)
		maxCandidateID = candidates[len(candidates)-1]
		inner, err := deleteLinkedGroupsTx(tx, providerValue, candidates, targets, true)
		if err != nil {
			return err
		}
		outcome = inner
		return nil
	})
	if err != nil {
		return repository.LinkedDeleteOutcome{}, 0, afterID, err
	}
	if outcome.Deleted > 0 {
		r.notifyInvalidation(ctx, repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged})
	}
	return outcome, candidateCount, maxCandidateID, nil
}

// CountCleanupWithLinked computes preview counts without materializing account IDs.
func (r *AccountRepository) CountCleanupWithLinked(ctx context.Context, providerValue account.Provider, statuses []string, now time.Time, targets []account.Provider) (repository.CleanupPreview, error) {
	preview := repository.CleanupPreview{
		RootsByStatus:    map[string]int64{},
		LinkedByProvider: map[account.Provider]int64{},
	}
	if !providerValue.IsValid() {
		return preview, fmt.Errorf("账号来源无效")
	}
	db := r.db.db.WithContext(ctx)
	for _, status := range statuses {
		if status != "disabled" && status != "reauthRequired" && status != "cooldown" {
			return preview, fmt.Errorf("不支持清理账号状态 %q", status)
		}
		var rootCount int64
		if err := applyAccountStatusFilter(
			db.Session(&gorm.Session{NewDB: true}).Model(&accountModel{}).Where("provider = ?", providerValue),
			status, now,
		).Count(&rootCount).Error; err != nil {
			return preview, err
		}
		preview.RootsByStatus[status] = rootCount
		preview.RootCount += rootCount
	}
	preview.Total = preview.RootCount
	return preview, nil
}

func uniqueSortedIDs(ids []uint64) []uint64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(ids))
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
