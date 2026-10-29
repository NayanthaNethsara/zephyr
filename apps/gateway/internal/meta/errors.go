package meta

import "github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"

type classification struct {
	code        string
	isRetryable bool
}

func classifyError(metaCode, httpStatus int) classification {
	switch metaCode {
	case 131047:
		return classification{core.CodeWindowClosed, false}
	case 131030:
		return classification{core.CodeRecipientNotAllowed, false}
	case 131026, 130403, 131050, 131049:
		return classification{core.CodeRecipientUndeliverable, false}
	case 131008, 131009, 131021, 131051, 100, 135000:
		return classification{core.CodeInvalidRequest, false}
	case 131052, 131053:
		return classification{core.CodeInvalidMedia, false}
	case 132000, 132001, 132005, 132007, 132012, 132015, 132016, 132068, 132069:
		return classification{core.CodeTemplateError, false}
	case 4, 80007, 130429, 131056:
		return classification{core.CodeRateLimited, true}
	case 131048, 131064, 368, 130497, 131031:
		return classification{core.CodeSendingRestricted, false}
	case 33, 131037, 131042, 131045, 133010:
		return classification{core.CodeSenderMisconfigured, false}
	case 0, 3, 10, 131005:
		return classification{core.CodeUpstreamAuthFailed, false}
	case 1, 2, 131000, 131016, 131057, 133004, 2494100:
		return classification{core.CodeUpstreamUnavailable, true}
	}
	if metaCode >= 190 && metaCode <= 299 {
		return classification{core.CodeUpstreamAuthFailed, false}
	}
	return classifyByStatus(httpStatus)
}

func classifyByStatus(httpStatus int) classification {
	switch {
	case httpStatus == 429:
		return classification{core.CodeRateLimited, true}
	case httpStatus >= 500:
		return classification{core.CodeUpstreamUnavailable, true}
	case httpStatus == 401 || httpStatus == 403:
		return classification{core.CodeUpstreamAuthFailed, false}
	default:
		return classification{core.CodeRejected, false}
	}
}
