package councilengine

import (
	"errors"
	"fmt"

	"bsl-flow/cli/internal/counciltransport"
)

// transportclassify.go maps counciltransport errors onto the BF_* dispatch
// markers the cycle classifies into terminal member states
// (Invoke-BSLFlowCouncilDispatchRole catch blocks). The mapping is shared by
// the managed stage host and the assisted `spec review` command.

// ClassifyTransportError converts a counciltransport error into an error whose
// message carries the exact marker dispatchRole recognizes.
func ClassifyTransportError(err error) error {
	switch {
	case errors.Is(err, counciltransport.ErrBeforeDispatch):
		return fmt.Errorf("%s: %s", MarkerNotDispatched, err)
	case errors.Is(err, counciltransport.ErrRedirectRefused):
		return fmt.Errorf("%s: council redirect was refused.", MarkerBeforeAcceptance)
	case errors.Is(err, counciltransport.ErrInvalidResponse):
		return fmt.Errorf("%s: %s", MarkerInvalidResponse, err)
	case errors.Is(err, counciltransport.ErrResponseBound):
		return fmt.Errorf("%s: council response exceeds the output limit.", MarkerInvalidResponse)
	case errors.Is(err, counciltransport.ErrUnknownAfterDispatch):
		return fmt.Errorf("BF_UNKNOWN_AFTER_DISPATCH: %s", err)
	}
	var statusErr *counciltransport.ProviderStatusError
	if errors.As(err, &statusErr) {
		if statusErr.Code >= 500 {
			return fmt.Errorf("BF_UNKNOWN_AFTER_DISPATCH: council provider returned a server error.")
		}
		return fmt.Errorf("%s: council request was not accepted by the provider.", MarkerBeforeAcceptance)
	}
	return fmt.Errorf("BF_UNKNOWN_AFTER_DISPATCH: council transport failed after dispatch: %s", err)
}

// MarkerFor returns the BF_* classification marker encoded in err, or
// BF_UNKNOWN_AFTER_DISPATCH when none of the known markers is present.
func MarkerFor(err error) string {
	if err == nil {
		return "BF_UNKNOWN_AFTER_DISPATCH"
	}
	text := err.Error()
	for _, marker := range []string{MarkerInvalidResponse, MarkerBeforeAcceptance, MarkerNotDispatched} {
		if containsMarker(text, marker) {
			return marker
		}
	}
	return "BF_UNKNOWN_AFTER_DISPATCH"
}

func containsMarker(text, marker string) bool {
	return len(marker) > 0 && (len(text) >= len(marker) && (text == marker || indexOf(text, marker) >= 0))
}

func indexOf(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}
