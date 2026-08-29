package server

import "encoding/json"

// Envelope is the wire shape of every WebSocket message in both directions:
// {"type": "...", "payload": {...}}.
type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// newEnvelope marshals a typed payload into a ready-to-send Envelope.
func newEnvelope(msgType string, payload interface{}) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{Type: msgType, Payload: raw})
}

// --- Client -> Server payloads ---

// JoinPayload is sent right after connecting. Name is only used when no
// existing participant is matched via the connection's ?token=.
type JoinPayload struct {
	Name string `json:"name"`
}

// HostStartPayload is sent by the host to lock the roster and deal the
// deck. RequiredCount is optional: 0 means "default to the locked
// headcount".
type HostStartPayload struct {
	Filters       Filters `json:"filters"`
	MaxMovies     int     `json:"maxMovies"`
	RequiredCount int     `json:"requiredCount"`
}

// SwipePayload records one participant's vote on one movie.
type SwipePayload struct {
	MovieID string `json:"movieID"`
	Dir     string `json:"dir"` // "yes" | "no"
}

// HostPickPayload is sent by the host to pick a winner from the leaderboard.
type HostPickPayload struct {
	MovieID string `json:"movieID"`
}

// --- Server -> Client payloads ---

// SessionStatePayload is the full snapshot sent to a client right after it
// joins. YourParticipantID/YourToken are only meaningful to the recipient —
// they are never included in the broadcast participant_update payload.
type SessionStatePayload struct {
	Status            Status            `json:"status"`
	Code              string            `json:"code"`
	RequiredCount     int               `json:"requiredCount"`
	Participants      []Participant     `json:"participants"`
	YourParticipantID string            `json:"yourParticipantId"`
	YourToken         string            `json:"yourToken"`
	YourVotes         map[string]string `json:"yourVotes,omitempty"`
}

// DeckPayload is the ordered, capped deck dealt at host:start.
type DeckPayload struct {
	Movies []Movie `json:"movies"`
}

// WarningPayload carries a non-fatal notice to one client. Unlike an error it
// does not abort the operation in progress; it is sent alongside a deck that
// was dealt from fewer sources than the host selected.
//
// Problems says which sources were missing and why. Message is a plain-text
// fallback: the client renders the problems itself, in its own wording.
type WarningPayload struct {
	Message  string          `json:"message"`
	Problems []SourceProblem `json:"problems,omitempty"`
}

// ParticipantUpdatePayload carries the current roster after a join, leave,
// or connection-state change.
type ParticipantUpdatePayload struct {
	Participants []Participant `json:"participants"`
}

// ProgressPayload is a lobby/HUD summary of swipe progress; it never reveals
// who voted which way on a specific movie.
type ProgressPayload struct {
	ParticipantsSwiped int `json:"participantsSwiped"`
	ParticipantsTotal  int `json:"participantsTotal"`
	CardsRemaining     int `json:"cardsRemaining"`
}

// MatchPayload announces the winning movie.
type MatchPayload struct {
	Movie Movie `json:"movie"`
}

// LeaderboardEntry is one row of the no-match leaderboard.
type LeaderboardEntry struct {
	Movie    Movie `json:"movie"`
	YesCount int   `json:"yesCount"`
}

// EndReason says why a session ended. It is a machine-readable value, not
// display text: the client owns the wording, exactly as it owns source labels.
type EndReason string

const (
	// EndReasonDeckExhausted means every participant swiped the whole deck
	// without a movie collecting enough yes votes.
	EndReasonDeckExhausted EndReason = "deck_exhausted"
	// EndReasonHostEnded means the host stopped the session early.
	EndReasonHostEnded EndReason = "host_ended"
	// EndReasonReconfigured means the server's sources changed underneath the
	// session, so the deck it was dealt no longer reflects what this
	// deployment offers.
	EndReasonReconfigured EndReason = "reconfigured"
)

// SessionEndedPayload is sent when a session ends without a match, whatever
// ended it.
type SessionEndedPayload struct {
	Leaderboard []LeaderboardEntry `json:"leaderboard"`
	Reason      EndReason          `json:"reason"`
}

// ErrorPayload reports a rejected action (e.g. joining a started session)
// back to the single client that caused it.
type ErrorPayload struct {
	Message string `json:"message"`
}
