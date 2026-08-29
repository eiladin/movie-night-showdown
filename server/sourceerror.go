package server

import (
	"errors"
	"fmt"
	"net/http"
)

// FailureReason says why a source could not be used. It is a machine-readable
// value, not display text: the client owns the wording, exactly as it owns
// source labels and end reasons.
//
// The distinction is the whole point. A rejected credential and a host that
// cannot be reached have different fixes, and sending an operator to the wrong
// one is worse than saying nothing at all.
type FailureReason string

const (
	// FailureUnauthorized means the upstream answered and refused the
	// credentials. The fix is a new API key or token.
	FailureUnauthorized FailureReason = "unauthorized"
	// FailureUnreachable means the upstream did not answer usefully — a
	// transport error, a timeout, or any other status this application cannot
	// read as a credential problem. The fix is on the media server or the
	// network between here and it.
	FailureUnreachable FailureReason = "unreachable"
	// FailureUnresolved means a library configured by name could not be turned
	// into an identifier, so no source was registered for it. It is not a query
	// failure: there was nothing to query.
	FailureUnresolved FailureReason = "unresolved"
)

// SourceError carries a failure's cause out of a source client, so the reason
// survives the trip to the handler that has to report it.
//
// The wrapped error keeps its original message for the log; only the reason
// crosses the wire.
type SourceError struct {
	Reason FailureReason
	Err    error
}

func (e *SourceError) Error() string {
	if e.Err == nil {
		return string(e.Reason)
	}
	return e.Err.Error()
}

func (e *SourceError) Unwrap() error { return e.Err }

// statusFailure wraps a non-2xx response as a SourceError whose reason names what
// the operator has to fix.
//
// Only 401 and 403 are credential problems. Everything else — a 500, a 404 from
// a reverse proxy, a gateway timeout — says the request did not get where it was
// meant to go, which is what "unreachable" means here.
func statusFailure(status int, err error) error {
	reason := FailureUnreachable
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		reason = FailureUnauthorized
	}
	return &SourceError{Reason: reason, Err: err}
}

// failureReason reads the reason out of an error, defaulting to unreachable.
//
// A bare error is a transport failure, a timeout, or a decode that never got a
// usable response — all of which are "the upstream did not answer", and none of
// which is a credential problem. Guessing "unauthorized" from an unclassified
// error would send an operator to rotate a key that was never the problem.
func failureReason(err error) FailureReason {
	var se *SourceError
	if errors.As(err, &se) {
		return se.Reason
	}
	return FailureUnreachable
}

// SourceProblem is one source this deployment could not use, in the shape the
// client renders. It replaces the bare list of source ids, which said that
// something was wrong without ever saying what.
//
// Label is the qualified form ("Jellyfin — Kids Movies"), because the host sees
// these in one flat list alongside the source picker; a card badge uses the bare
// service name and is a different field (see Availability.Label).
//
// Source is empty for a library configured by name that could not be resolved.
// That is not an oversight: such a library deliberately has no SourceID, since
// an unresolved name would put a space in a value that is a path segment in the
// image proxy.
type SourceProblem struct {
	Label  string        `json:"label"`
	Source SourceID      `json:"source,omitempty"`
	Reason FailureReason `json:"reason"`
}

// sourceProblem describes one source's failure.
func sourceProblem(src MovieSource, err error) SourceProblem {
	return SourceProblem{
		Label:  sourceLabel(src),
		Source: src.ID(),
		Reason: failureReason(err),
	}
}

// pendingProblems reports the libraries this set holds that are configured but
// unresolved, so a deployment whose media server rejected its credentials is not
// left with an empty picker and no explanation.
//
// The set reports them rather than registering them: an unresolvable name is not
// a source, and never becomes one.
func (set *sourceSet) pendingProblems() []SourceProblem {
	if set == nil {
		return []SourceProblem{}
	}
	return pendingProblems(set.pending)
}

// pendingProblems describes a list of unresolved libraries. The Client keeps its
// own snapshot of them, so this takes the slice rather than the set.
func pendingProblems(pending []pendingLibrary) []SourceProblem {
	out := make([]SourceProblem, 0, len(pending))
	for _, p := range pending {
		out = append(out, SourceProblem{
			Label:  libraryScopedName(p.serviceName, libraryRef{ID: p.name, Name: p.name}),
			Reason: FailureUnresolved,
		})
	}
	return out
}

// appendProblems joins two problem lists, always returning a non-nil slice so
// the JSON carries [] rather than null.
func appendProblems(a, b []SourceProblem) []SourceProblem {
	out := make([]SourceProblem, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

// problemLabels renders a problem list for a log line.
func problemLabels(problems []SourceProblem) string {
	out := ""
	for i, p := range problems {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s (%s)", p.Label, p.Reason)
	}
	return out
}
