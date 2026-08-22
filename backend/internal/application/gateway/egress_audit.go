package gateway

import (
	accountdomain "M365Copilot2ApiX/backend/internal/domain/account"
	"M365Copilot2ApiX/backend/internal/domain/audit"
	egressdomain "M365Copilot2ApiX/backend/internal/domain/egress"
	"M365Copilot2ApiX/backend/internal/domain/media"
	infraegress "M365Copilot2ApiX/backend/internal/infra/egress"
)

func applyAuditEgress(record *audit.Record, trace *infraegress.Trace, provider accountdomain.Provider) {
	selection, ok := trace.Selection(primaryEgressScope(provider))
	if !ok {
		return
	}
	record.EgressNodeName = selection.NodeName
	record.EgressScope = string(selection.Scope)
	if selection.Proxied {
		record.EgressMode = audit.EgressModeProxy
	} else {
		record.EgressMode = audit.EgressModeDirect
	}
	if selection.NodeID != 0 {
		id := selection.NodeID
		record.EgressNodeID = &id
	}
}

func applyMediaJobEgress(job *media.Job, trace *infraegress.Trace, provider accountdomain.Provider) {
	selection, ok := trace.Selection(primaryEgressScope(provider))
	if !ok {
		return
	}
	job.EgressNodeName = selection.NodeName
	job.EgressScope = string(selection.Scope)
	job.EgressMode = string(audit.EgressModeDirect)
	if selection.Proxied {
		job.EgressMode = string(audit.EgressModeProxy)
	}
	if selection.NodeID != 0 {
		id := selection.NodeID
		job.EgressNodeID = &id
	}
}

func primaryEgressScope(provider accountdomain.Provider) egressdomain.Scope {
	return egressdomain.ScopeBuild
}
