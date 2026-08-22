package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	accountdomain "M365Copilot2ApiX/backend/internal/domain/account"
	"M365Copilot2ApiX/backend/internal/infra/provider"
	"M365Copilot2ApiX/backend/internal/infra/security"
	"M365Copilot2ApiX/backend/internal/pkg/batch"
	"M365Copilot2ApiX/backend/internal/repository"
)

func (s *Service) StartDeviceLogin(ctx context.Context) (DeviceStartResult, error) {
	adapter, ok := s.providers.DeviceOAuth(accountdomain.ProviderM365)
	if !ok {
		return DeviceStartResult{}, fmt.Errorf("M365 Provider 未注册")
	}
	authorization, err := adapter.StartDeviceAuthorization(ctx)
	if err != nil {
		return DeviceStartResult{}, err
	}
	sessionID, err := security.NewOpaqueToken(18)
	if err != nil {
		return DeviceStartResult{}, err
	}
	now := time.Now().UTC()
	session := accountdomain.DeviceSession{ID: sessionID, DeviceCode: authorization.DeviceCode, UserCode: authorization.UserCode, VerificationURI: authorization.VerificationURI, VerificationURIComplete: authorization.VerificationURIComplete, Interval: authorization.Interval, NextPollAt: now.Add(authorization.Interval), ExpiresAt: now.Add(authorization.ExpiresIn)}
	if err := s.deviceSessions.Create(ctx, session); err != nil {
		return DeviceStartResult{}, err
	}
	return DeviceStartResult{SessionID: sessionID, UserCode: session.UserCode, VerificationURI: session.VerificationURI, VerificationURIComplete: session.VerificationURIComplete, Interval: session.Interval, ExpiresAt: session.ExpiresAt}, nil
}

// PollDeviceLogin 执行一次上游轮询，成功后立即加密并写入账号仓储。
func (s *Service) PollDeviceLogin(ctx context.Context, sessionID string) (View, error) {
	now := time.Now().UTC()
	session, err := s.deviceSessions.Get(ctx, sessionID, now)
	if err != nil {
		return View{}, ErrDeviceDenied
	}
	if now.Before(session.NextPollAt) {
		return View{}, ErrDeviceSlowDown
	}
	adapter, ok := s.providers.DeviceOAuth(accountdomain.ProviderM365)
	if !ok {
		return View{}, fmt.Errorf("M365 Provider 未注册")
	}
	seed, err := adapter.PollDeviceAuthorization(ctx, session.DeviceCode)
	session.NextPollAt = now.Add(session.Interval)
	_ = s.deviceSessions.Update(ctx, session)
	if errors.Is(err, provider.ErrAuthorizationPending) {
		return View{}, ErrDevicePending
	}
	if errors.Is(err, provider.ErrSlowDown) {
		session.Interval += 5 * time.Second
		session.NextPollAt = now.Add(session.Interval)
		_ = s.deviceSessions.Update(ctx, session)
		return View{}, ErrDeviceSlowDown
	}
	if errors.Is(err, provider.ErrAuthorizationDenied) {
		_ = s.deviceSessions.Delete(ctx, sessionID)
		return View{}, ErrDeviceDenied
	}
	if err != nil {
		return View{}, err
	}
	value, _, err := s.persistSeed(ctx, seed)
	if err != nil {
		return View{}, err
	}
	_ = s.deviceSessions.Delete(ctx, sessionID)
	return s.Get(ctx, value.ID)
}

// ImportCredentials 导入用户上传的 OAuth 账号凭据。
func (s *Service) ImportCredentials(ctx context.Context, data []byte) (ImportResult, error) {
	return s.ImportCredentialsWithObserver(ctx, data, nil)
}

func (s *Service) ImportCredentialsWithObserver(ctx context.Context, data []byte, observer ImportedAccountObserver) (ImportResult, error) {
	return s.ImportCredentialsWithProgress(ctx, data, observer, nil)
}

// ImportCredentialsWithProgress 导入 Build 凭据并报告已写入流水线的账号数。
func (s *Service) ImportCredentialsWithProgress(ctx context.Context, data []byte, observer ImportedAccountObserver, progress BatchProgressObserver) (ImportResult, error) {
	return s.ImportCredentialDocumentsWithProgress(ctx, [][]byte{data}, observer, progress)
}

// ImportCredentialDocumentsWithProgress 合并解析多个 Build 凭据文件，并作为一个批次写入和同步。
func (s *Service) ImportCredentialDocumentsWithProgress(ctx context.Context, documents [][]byte, observer ImportedAccountObserver, progress BatchProgressObserver) (ImportResult, error) {
	adapter, ok := s.providers.CredentialCodec(accountdomain.ProviderM365)
	if !ok {
		return ImportResult{}, fmt.Errorf("M365 Provider 未注册")
	}
	return s.importCredentialDocumentsWithProgress(ctx, adapter, documents, observer, progress)
}

// ImportWebCredentials 导入版本化或旧号池格式的 Grok Web SSO 凭据。
func (s *Service) importCredentialDocumentsWithProgress(ctx context.Context, adapter provider.CredentialCodecAdapter, documents [][]byte, observer ImportedAccountObserver, progress BatchProgressObserver) (ImportResult, error) {
	if len(documents) == 0 {
		return ImportResult{}, fmt.Errorf("%w: 没有可导入的账号文件", ErrInvalidImport)
	}
	seeds := make([]provider.CredentialSeed, 0)
	seen := make(map[string]struct{})
	parsedAccounts := 0
	skipped := 0
	for index, document := range documents {
		values, err := adapter.ParseImportedCredentials(document)
		if err != nil {
			if errors.Is(err, provider.ErrCredentialLimit) {
				return ImportResult{}, fmt.Errorf("%w: 单次最多导入 %d 个账号", ErrImportLimit, maxCredentialImportAccounts)
			}
			return ImportResult{}, fmt.Errorf("%w: 第 %d 个文件: %v", ErrInvalidImport, index+1, err)
		}
		parsedAccounts += len(values)
		if parsedAccounts > maxCredentialImportAccounts {
			return ImportResult{}, fmt.Errorf("%w: 单次最多导入 %d 个账号", ErrImportLimit, maxCredentialImportAccounts)
		}
		for _, value := range values {
			if value.SourceKey != "" {
				key := string(value.Provider) + "\x00" + value.SourceKey
				if _, exists := seen[key]; exists {
					skipped++
					continue
				}
				seen[key] = struct{}{}
			}
			seeds = append(seeds, value)
		}
	}
	var result ImportResult
	var err error
	if preparer, ok := adapter.(provider.CredentialImportPreparer); ok && hasRefreshTokenOnlySeed(seeds) {
		result, err = s.persistPreparedImportedSeeds(ctx, seeds, preparer, observer, progress)
	} else {
		result, err = s.persistImportedSeeds(ctx, seeds, observer, progress)
	}
	result.Skipped += skipped
	return result, err
}

func hasRefreshTokenOnlySeed(seeds []provider.CredentialSeed) bool {
	for _, seed := range seeds {
		if strings.TrimSpace(seed.AccessToken) == "" && strings.TrimSpace(seed.RefreshToken) != "" {
			return true
		}
	}
	return false
}

func (s *Service) persistPreparedImportedSeeds(ctx context.Context, seeds []provider.CredentialSeed, preparer provider.CredentialImportPreparer, observer ImportedAccountObserver, progress BatchProgressObserver) (ImportResult, error) {
	result := ImportResult{AccountIDs: make([]uint64, 0, len(seeds))}
	if progress != nil {
		if err := progress(0, len(seeds)); err != nil {
			return result, err
		}
	}
	prepareCtx, cancelPrepare := context.WithCancel(ctx)
	defer cancelPrepare()
	var (
		mu        sync.Mutex
		firstErr  error
		completed int
		persisted bool
		seen      = make(map[string]struct{}, len(seeds))
	)
	_, batchErr := batch.ForEachObserved(prepareCtx, seeds, batch.Options{Workers: credentialImportPrepareWorkers}, func(itemCtx context.Context, seed provider.CredentialSeed) (provider.CredentialSeed, error) {
		if strings.TrimSpace(seed.AccessToken) == "" && strings.TrimSpace(seed.RefreshToken) != "" {
			return preparer.PrepareImportedCredential(itemCtx, seed)
		}
		return seed, nil
	}, func(index int, item batch.Result[provider.CredentialSeed]) {
		mu.Lock()
		defer mu.Unlock()
		completed++
		if !item.Completed || item.Err != nil {
			result.Failed++
			if item.Err != nil {
				s.logger.Warn("account_rt_import_failed", "index", index+1, "error", item.Err)
			}
			reportCredentialImportProgress(progress, completed, len(seeds), &firstErr, cancelPrepare)
			return
		}
		seed := item.Value
		if seed.SourceKey != "" {
			key := string(seed.Provider) + "\x00" + seed.SourceKey
			if _, exists := seen[key]; exists {
				result.Skipped++
				reportCredentialImportProgress(progress, completed, len(seeds), &firstErr, cancelPrepare)
				return
			}
			seen[key] = struct{}{}
		}

		// OAuth providers may invalidate the submitted refresh token as soon as
		// they return its replacement. Persist that replacement before any
		// request-scoped observer or progress callback can abort the import.
		persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), credentialStateWriteTimeout)
		stored, err := s.persistImportedSeed(persistCtx, seed)
		cancelPersist()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			cancelPrepare()
			return
		}
		persisted = true
		result.AccountIDs = append(result.AccountIDs, stored.ID)
		if stored.Created {
			result.Created++
		} else {
			result.Updated++
		}
		if firstErr == nil && observer != nil {
			if err := observer(stored.ID); err != nil {
				firstErr = err
				cancelPrepare()
			}
		}
		reportCredentialImportProgress(progress, completed, len(seeds), &firstErr, cancelPrepare)
	})
	if persisted {
		s.WakeCredentialRefresh()
	}
	return result, errors.Join(firstErr, batchErr)
}

func reportCredentialImportProgress(progress BatchProgressObserver, completed, total int, firstErr *error, cancel context.CancelFunc) {
	if progress == nil || *firstErr != nil {
		return
	}
	if err := progress(completed, total); err != nil {
		*firstErr = err
		cancel()
	}
}

func (s *Service) persistImportedSeed(ctx context.Context, seed provider.CredentialSeed) (repository.AccountUpsertResult, error) {
	value, err := s.credentialFromSeed(seed)
	if err != nil {
		return repository.AccountUpsertResult{}, err
	}
	stored, err := s.accounts.UpsertManyByIdentity(ctx, []accountdomain.Credential{value})
	if err != nil {
		return repository.AccountUpsertResult{}, err
	}
	if len(stored) != 1 {
		return repository.AccountUpsertResult{}, fmt.Errorf("导入账号持久化结果数量无效: %d", len(stored))
	}
	return stored[0], nil
}

func (s *Service) persistImportedSeeds(ctx context.Context, seeds []provider.CredentialSeed, observer ImportedAccountObserver, progress BatchProgressObserver) (ImportResult, error) {
	return s.persistImportedSeedsFromProgress(ctx, seeds, observer, progress, 0, len(seeds), true)
}

func (s *Service) persistImportedSeedsFromProgress(ctx context.Context, seeds []provider.CredentialSeed, observer ImportedAccountObserver, progress BatchProgressObserver, completed, total int, reportInitial bool) (ImportResult, error) {
	result := ImportResult{AccountIDs: make([]uint64, 0, len(seeds))}
	if progress != nil && reportInitial {
		if err := progress(completed, total); err != nil {
			return ImportResult{}, err
		}
	}
	for start := 0; start < len(seeds); start += credentialImportChunkSize {
		end := min(start+credentialImportChunkSize, len(seeds))
		values := make([]accountdomain.Credential, 0, end-start)
		for _, seed := range seeds[start:end] {
			value, err := s.credentialFromSeed(seed)
			if err != nil {
				return ImportResult{}, err
			}
			values = append(values, value)
		}
		stored, err := s.accounts.UpsertManyByIdentity(ctx, values)
		if err != nil {
			return ImportResult{}, err
		}
		for _, value := range stored {
			result.AccountIDs = append(result.AccountIDs, value.ID)
			if observer != nil {
				if err := observer(value.ID); err != nil {
					return ImportResult{}, err
				}
			}
			completed++
			if progress != nil {
				if err := progress(completed, total); err != nil {
					return ImportResult{}, err
				}
			}
			if value.Created {
				result.Created++
			} else {
				result.Updated++
			}
		}
	}
	s.WakeCredentialRefresh()
	return result, nil
}

func offsetBatchProgress(progress BatchProgressObserver, offset, total int) BatchProgressObserver {
	if progress == nil {
		return nil
	}
	return func(completed, _ int) error {
		if completed == 0 {
			return nil
		}
		return progress(offset+completed, total)
	}
}

func (s *Service) ExportCredentials(ctx context.Context) (ExportResult, error) {
	return s.ExportProviderCredentials(ctx, accountdomain.ProviderM365)
}

// ExportProviderCredentials 导出可由对应 Provider 导入接口重新读取的凭据文档。
func (s *Service) ExportProviderCredentials(ctx context.Context, providerValue accountdomain.Provider) (ExportResult, error) {
	return s.exportProviderCredentials(ctx, providerValue, repository.AccountListQuery{
		Page:   repository.PageQuery{Limit: maxCredentialExportAccounts + 1},
		Filter: repository.AccountListFilter{Provider: string(providerValue), Now: s.now()},
	}, true, 0)
}

// ExportProviderCredentialsCursor exports a stable provider batch bounded by
// the maximum account ID captured by the first request.
func (s *Service) ExportProviderCredentialsCursor(ctx context.Context, providerValue accountdomain.Provider, afterID, snapshotMaxID uint64, limit int) (ExportPageResult, error) {
	if limit < 1 || limit > maxCredentialExportAccounts {
		return ExportPageResult{}, invalidInput("单批导出数量必须在 1 到 10000 之间")
	}
	if afterID > 0 && snapshotMaxID == 0 {
		return ExportPageResult{}, invalidInput("继续导出时必须提供快照上界")
	}
	if snapshotMaxID > 0 && afterID > snapshotMaxID {
		return ExportPageResult{}, invalidInput("导出游标不能超过快照上界")
	}
	if !providerValue.IsValid() {
		return ExportPageResult{}, invalidInput("账号来源无效")
	}
	if snapshotMaxID == 0 {
		values, _, err := s.accounts.List(ctx, repository.AccountListQuery{
			Page:   repository.PageQuery{Limit: 1, Sort: repository.SortQuery{Field: "id", Direction: repository.SortDescending}},
			Filter: repository.AccountListFilter{Provider: string(providerValue), Now: s.now()},
		})
		if err != nil {
			return ExportPageResult{}, err
		}
		if len(values) == 0 {
			result, exportErr := s.marshalProviderCredentials(providerValue, nil)
			return ExportPageResult{ExportResult: result}, exportErr
		}
		snapshotMaxID = values[0].ID
	}
	values, total, err := s.accounts.List(ctx, repository.AccountListQuery{
		Page: repository.PageQuery{Limit: limit, Sort: repository.SortQuery{Field: "id", Direction: repository.SortAscending}},
		Filter: repository.AccountListFilter{
			Provider: string(providerValue), AfterID: afterID, ThroughID: snapshotMaxID, Now: s.now(),
		},
	})
	if err != nil {
		return ExportPageResult{}, err
	}
	result, err := s.marshalProviderCredentials(providerValue, values)
	if err != nil {
		return ExportPageResult{}, err
	}
	nextID := afterID
	if len(values) > 0 {
		nextID = values[len(values)-1].ID
	}
	return ExportPageResult{
		ExportResult: result, NextID: nextID, SnapshotMaxID: snapshotMaxID, HasMore: total > int64(len(values)),
	}, nil
}

// ExportProviderCredentialsByIDs 只导出管理端明确选择且属于指定 Provider 的账号。
func (s *Service) ExportProviderCredentialsByIDs(ctx context.Context, providerValue accountdomain.Provider, ids []uint64) (ExportResult, error) {
	values, err := normalizeIDs(ids, maxCredentialExportAccounts)
	if err != nil {
		return ExportResult{}, err
	}
	return s.exportProviderCredentials(ctx, providerValue, repository.AccountListQuery{
		Page: repository.PageQuery{Limit: len(values)},
		Filter: repository.AccountListFilter{
			Provider: string(providerValue), AccountIDs: values, RestrictIDs: true, Now: s.now(),
		},
	}, false, len(values))
}

func (s *Service) exportProviderCredentials(ctx context.Context, providerValue accountdomain.Provider, query repository.AccountListQuery, enforceTotalLimit bool, expectedCount int) (ExportResult, error) {
	if !providerValue.IsValid() {
		return ExportResult{}, invalidInput("账号来源无效")
	}
	values, total, err := s.accounts.List(ctx, query)
	if err != nil {
		return ExportResult{}, err
	}
	if enforceTotalLimit && total > maxCredentialExportAccounts {
		return ExportResult{}, fmt.Errorf("%w: 单次最多导出 10000 个账号", ErrExportLimit)
	}
	if err := validateCredentialExportCount(expectedCount, total, len(values)); err != nil {
		return ExportResult{}, err
	}
	return s.marshalProviderCredentials(providerValue, values)
}

func validateCredentialExportCount(expected int, total int64, actual int) error {
	if expected > 0 && (total != int64(expected) || actual != expected) {
		return invalidInput("所选账号包含不存在或不属于当前号池的账号")
	}
	return nil
}

func (s *Service) marshalProviderCredentials(providerValue accountdomain.Provider, values []accountdomain.Credential) (ExportResult, error) {
	if !providerValue.IsValid() {
		return ExportResult{}, invalidInput("账号来源无效")
	}
	if s.providers == nil {
		return ExportResult{}, fmt.Errorf("Provider 注册表未初始化")
	}
	adapter, ok := s.providers.CredentialCodec(providerValue)
	if !ok {
		return ExportResult{}, fmt.Errorf("Provider %s 不支持凭据导出", providerValue)
	}
	var err error
	seeds := make([]provider.CredentialSeed, 0, len(values))
	for _, value := range values {
		if value.Provider != providerValue {
			continue
		}
		accessToken := ""
		if value.EncryptedAccessToken != "" {
			accessToken, err = s.cipher.Decrypt(value.EncryptedAccessToken)
			if err != nil {
				return ExportResult{}, fmt.Errorf("解密账号 %d access token: %w", value.ID, err)
			}
		}
		refreshToken := ""
		if value.EncryptedRefreshToken != "" {
			refreshToken, err = s.cipher.Decrypt(value.EncryptedRefreshToken)
			if err != nil {
				return ExportResult{}, fmt.Errorf("解密账号 %d refresh token: %w", value.ID, err)
			}
		}
		if accessToken == "" && refreshToken == "" {
			return ExportResult{}, fmt.Errorf("账号 %d 没有可导出的凭据", value.ID)
		}
		seeds = append(seeds, provider.CredentialSeed{
			Provider: value.Provider, AuthType: value.AuthType,
			Name: value.Name, Email: value.Email, UserID: value.UserID, TeamID: value.TeamID,
			OIDCClientID: value.OIDCClientID, AccessToken: accessToken, RefreshToken: refreshToken,
			ExpiresAt: value.ExpiresAt,
		})
	}
	data, err := adapter.MarshalCredentials(seeds)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{Data: data, Count: len(seeds)}, nil
}
