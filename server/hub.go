package server

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 8192
	sendBufferSize = 16
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// LAN-only app with no auth; there is nothing origin-checking would
	// protect here.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Client is one WebSocket connection. Exactly one goroutine (writePump)
// writes to conn and exactly one goroutine (readPump) reads from it — never
// write to conn from anywhere else.
type Client struct {
	conn          *websocket.Conn
	send          chan []byte
	done          chan struct{} // closed by readPump on exit; signals writePump to stop
	session       *Session
	sources       map[SourceID]MovieSource // used by handleHostStart to deal the deck
	order         []SourceID               // canonical source order for selectSources
	participantID string                   // set once join() attaches this client to a participant
	token         string                   // from ?token=; used to match/resume a participant
}

// handleWS upgrades GET /ws?code=&token= to a WebSocket connection and
// starts the client's read/write pumps.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("code")))
	token := r.URL.Query().Get("token")

	session, ok := s.store.Get(code)
	if !ok {
		http.Error(w, "unknown session code", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws: upgrade failed: %v", err)
		return
	}

	client := &Client{
		conn:    conn,
		send:    make(chan []byte, sendBufferSize),
		done:    make(chan struct{}),
		session: session,
		sources: s.sources,
		order:   s.order,
		token:   token,
	}

	go client.writePump()
	go client.readPump()
}

// readPump is the only goroutine that reads from conn. It dispatches
// messages and, on exit (error/close), detaches the client from its session
// and signals writePump to stop by closing done.
//
// It closes done — never send. Broadcasters send on send from other
// goroutines (see trySend); if the reader closed send, an in-flight broadcast
// that snapshotted this client under the lock could send on a closed channel
// and panic the process. done is closed exactly once, here, and only ever
// selected on; send is never closed, so a concurrent trySend is always safe.
func (c *Client) readPump() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ws: readPump panic for participant %s: %v", c.participantID, r)
		}
		c.session.removeClient(c)
		close(c.done)
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			log.Printf("ws: invalid envelope: %v", err)
			continue
		}
		c.handleMessage(env)
	}
}

// writePump is the only goroutine that writes to conn.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ws: writePump panic for participant %s: %v", c.participantID, r)
		}
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case <-c.done:
			// readPump has detached this client; tell the peer and stop.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		case msg := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// trySend enqueues a message for writePump without ever blocking the
// caller. A full buffer means an unusually slow client; the message is
// dropped rather than stalling the broadcaster.
func (c *Client) trySend(data []byte) {
	// send is never closed (readPump closes done instead), so this send can
	// never panic even if this client is disconnecting concurrently. The done
	// case lets a detached client shed queued messages instead of filling its
	// buffer after writePump has stopped reading.
	select {
	case <-c.done:
	case c.send <- data:
	default:
		log.Printf("ws: send buffer full for participant %s, dropping message", c.participantID)
	}
}

func (c *Client) sendJSON(msgType string, payload interface{}) {
	data, err := newEnvelope(msgType, payload)
	if err != nil {
		log.Printf("ws: marshal %s: %v", msgType, err)
		return
	}
	c.trySend(data)
}

func (c *Client) sendError(message string) {
	c.sendJSON("error", ErrorPayload{Message: message})
}

func (c *Client) handleMessage(env Envelope) {
	switch env.Type {
	case "join":
		c.handleJoin(env.Payload)
	case "host:start":
		c.handleHostStart(env.Payload)
	case "swipe":
		c.handleSwipe(env.Payload)
	case "undo":
		c.handleUndo(env.Payload)
	case "host:pick":
		c.handleHostPick(env.Payload)
	case "host:end":
		c.handleHostEnd(env.Payload)
	default:
		log.Printf("ws: unhandled message type %q", env.Type)
	}
}

// handleJoin attaches this connection to a participant: an existing one if
// c.token matches, otherwise a new one (only while the session is still in
// the Lobby). It then sends session_state to this client and broadcasts
// participant_update to everyone.
func (c *Client) handleJoin(raw json.RawMessage) {
	var p JoinPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		c.sendError("invalid join payload")
		return
	}

	session := c.session
	session.mu.Lock()

	participant := findParticipantByTokenLocked(session, c.token)
	if participant == nil {
		if session.Status != StatusLobby {
			session.mu.Unlock()
			c.sendError("session already started")
			return
		}
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = "Guest"
		}
		participant = &Participant{
			ID:    uuid.NewString(),
			Name:  name,
			Token: uuid.NewString(),
		}
		session.Participants[participant.ID] = participant
	}

	participant.Connected = true
	c.participantID = participant.ID
	c.token = participant.Token
	session.clients[participant.ID] = c

	yourVotes := make(map[string]string)
	for movieID, votes := range session.Votes {
		if yes, ok := votes[participant.ID]; ok {
			if yes {
				yourVotes[movieID] = "yes"
			} else {
				yourVotes[movieID] = "no"
			}
		}
	}

	state := SessionStatePayload{
		Status:            session.Status,
		Code:              session.Code,
		RequiredCount:     session.RequiredCount,
		Participants:      participantViewsLocked(session),
		YourParticipantID: participant.ID,
		YourToken:         participant.Token,
		YourVotes:         yourVotes,
	}

	var deck []Movie
	var match *Movie
	var lb []LeaderboardEntry

	if session.Status != StatusLobby {
		deck = make([]Movie, len(session.Deck))
		copy(deck, session.Deck)
	}

	if session.Status == StatusMatched {
		match = session.findMovie(session.WinnerID)
	} else if session.Status == StatusEnded {
		lb = session.checkSessionEndedLocked()
	}

	session.mu.Unlock()

	c.sendJSON("session_state", state)

	if deck != nil {
		c.sendJSON("deck", DeckPayload{Movies: deck})
	}
	if match != nil {
		c.sendJSON("match", MatchPayload{Movie: *match})
	}
	if lb != nil {
		c.sendJSON("session_ended", SessionEndedPayload{Leaderboard: lb})
	}
	session.broadcastParticipants()
}

// handleHostStart locks the current roster, deals a shuffled+capped deck
// from Jellyfin, and activates the session. Only the host may call this; it
// is rejected once the roster is already locked.
func (c *Client) handleHostStart(raw json.RawMessage) {
	var p HostStartPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		c.sendError("invalid host:start payload")
		return
	}

	session := c.session

	session.mu.Lock()
	if c.participantID != session.HostID {
		session.mu.Unlock()
		c.sendError("only the host can start the session")
		return
	}
	if session.Locked {
		session.mu.Unlock()
		c.sendError("session already started")
		return
	}
	session.mu.Unlock()

	// The source fetches are blocking network calls; they must run without
	// holding session.mu so they can never stall other participants. Sources are
	// queried concurrently, so this costs one round trip rather than one per
	// source.
	sources := selectSources(c.sources, p.Filters.Sources, c.order)
	movies, failed, err := gatherShoe(context.Background(), sources, p.Filters)
	if err != nil {
		log.Printf("host:start: every selected source failed")
		c.sendError("failed to load movies from any selected source")
		return
	}
	if len(failed) > 0 {
		names := make([]string, len(failed))
		for i, f := range failed {
			names[i] = string(f)
		}
		c.sendJSON("warning", WarningPayload{
			Message: "Could not reach: " + strings.Join(names, ", ") + ". Dealt from the rest.",
			Sources: failed,
		})
	}
	if len(movies) == 0 {
		c.sendError("no movies matched those filters on the selected sources")
		return
	}
	rand.Shuffle(len(movies), func(i, j int) { movies[i], movies[j] = movies[j], movies[i] })

	maxMovies := p.MaxMovies
	if maxMovies <= 0 {
		maxMovies = defaultDeckSize
	}
	if len(movies) > maxMovies {
		movies = movies[:maxMovies]
	}

	session.mu.Lock()
	if c.participantID != session.HostID {
		session.mu.Unlock()
		c.sendError("only the host can start the session")
		return
	}
	if session.Locked {
		session.mu.Unlock()
		c.sendError("session already started")
		return
	}
	rosterCount := len(session.Participants)
	session.Locked = true
	if p.RequiredCount >= 1 && p.RequiredCount <= rosterCount {
		session.RequiredCount = p.RequiredCount
	} else {
		session.RequiredCount = rosterCount
	}
	session.Deck = movies
	session.Status = StatusActive
	session.mu.Unlock()

	session.broadcastDeck()
	session.broadcastSessionState()
}

// handleSwipe records one vote and, on a match, transitions the session to
// Matched and broadcasts it; otherwise it broadcasts updated progress. A
// "no" is a secret-kill: it is recorded but never removed from any client's
// deck.
func (c *Client) handleSwipe(raw json.RawMessage) {
	var p SwipePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		c.sendError("invalid swipe payload")
		return
	}
	if p.MovieID == "" {
		c.sendError("movieID is required")
		return
	}
	if p.Dir != "yes" && p.Dir != "no" {
		c.sendError(`dir must be "yes" or "no"`)
		return
	}
	yes := p.Dir == "yes"

	session := c.session
	session.mu.Lock()
	if session.Status != StatusActive {
		session.mu.Unlock()
		c.sendError("session is not active")
		return
	}
	if session.findMovie(p.MovieID) == nil {
		session.mu.Unlock()
		c.sendError("unknown movie")
		return
	}
	winner, matched := session.recordSwipe(c.participantID, p.MovieID, yes)
	var progress ProgressPayload
	var lb []LeaderboardEntry
	if matched {
		session.Status = StatusMatched
		session.WinnerID = p.MovieID
	} else {
		progress = session.progressLocked(p.MovieID)
		lb = session.checkSessionEndedLocked()
		if lb != nil {
			session.Status = StatusEnded
		}
	}
	session.mu.Unlock()

	if matched {
		session.broadcast("match", MatchPayload{Movie: *winner})
		return
	}
	if lb != nil {
		session.broadcast("session_ended", SessionEndedPayload{Leaderboard: lb})
		return
	}
	session.broadcast("progress", progress)
}

// handleUndo reverses the sender's last vote: it deletes their entry from
// Votes[movieID] and clears LastSwipe, which can revive a secretly-killed
// movie. No-op if the sender has not swiped yet.
func (c *Client) handleUndo(raw json.RawMessage) {
	session := c.session
	session.mu.Lock()
	if session.Status != StatusActive {
		session.mu.Unlock()
		c.sendError("session is not active")
		return
	}
	last, ok := session.LastSwipe[c.participantID]
	if !ok {
		session.mu.Unlock()
		return
	}
	if votes, ok := session.Votes[last.MovieID]; ok {
		delete(votes, c.participantID)
	}
	delete(session.LastSwipe, c.participantID)
	progress := session.progressLocked(last.MovieID)
	session.mu.Unlock()

	session.broadcast("progress", progress)
}

// handleHostPick allows the host to manually pick a winner from the leaderboard
// when the session has ended with no match.
func (c *Client) handleHostPick(raw json.RawMessage) {
	var p HostPickPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		c.sendError("invalid host:pick payload")
		return
	}
	session := c.session
	session.mu.Lock()
	if c.participantID != session.HostID {
		session.mu.Unlock()
		c.sendError("only the host can pick a winner")
		return
	}
	if session.Status != StatusEnded {
		session.mu.Unlock()
		c.sendError("session is not ended")
		return
	}
	movie := session.findMovie(p.MovieID)
	if movie == nil {
		session.mu.Unlock()
		c.sendError("movie not found")
		return
	}
	session.Status = StatusMatched
	session.WinnerID = movie.ID
	session.mu.Unlock()

	session.broadcast("match", MatchPayload{Movie: *movie})
}

// handleHostEnd lets the host force-end an active session, jumping straight
// to the leaderboard built from the votes cast so far. Only the host may do it.
func (c *Client) handleHostEnd(raw json.RawMessage) {
	session := c.session
	session.mu.Lock()
	if c.participantID != session.HostID {
		session.mu.Unlock()
		c.sendError("only the host can end the session")
		return
	}
	if session.Status != StatusActive {
		session.mu.Unlock()
		c.sendError("session is not active")
		return
	}
	session.Status = StatusEnded
	lb := session.buildLeaderboardLocked()
	session.mu.Unlock()

	session.broadcast("session_ended", SessionEndedPayload{Leaderboard: lb})
}

// findParticipantByTokenLocked returns the participant whose Token matches,
// or nil if token is empty or unmatched. Caller must hold session.mu.
func findParticipantByTokenLocked(session *Session, token string) *Participant {
	if token == "" {
		return nil
	}
	for _, p := range session.Participants {
		if p.Token == token {
			return p
		}
	}
	return nil
}

// participantViewsLocked returns a stable-ordered, wire-safe copy of the
// roster (Token is excluded via its json:"-" tag). Caller must hold session.mu.
func participantViewsLocked(session *Session) []Participant {
	views := make([]Participant, 0, len(session.Participants))
	for _, p := range session.Participants {
		views = append(views, *p)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}

// removeClient detaches c from its session if it is still the active
// connection for its participant, marks the participant disconnected, and
// broadcasts the updated roster. The participant record itself is kept so
// it can resume later with its token.
func (s *Session) removeClient(c *Client) {
	s.mu.Lock()
	if c.participantID == "" {
		s.mu.Unlock()
		return
	}
	current, ok := s.clients[c.participantID]
	if !ok || current != c {
		// A newer connection already replaced this one (fast reconnect);
		// leave that connection's state alone.
		s.mu.Unlock()
		return
	}
	delete(s.clients, c.participantID)
	if p, ok := s.Participants[c.participantID]; ok {
		p.Connected = false
	}
	s.mu.Unlock()

	// A disconnect never ends the session: it ends only when the connected
	// players finish swiping (handleSwipe) or the host ends it explicitly
	// (handleHostEnd). This keeps a brief network blip from prematurely
	// ending everyone's night.
	s.broadcastParticipants()
}

// broadcastParticipants sends the current roster to every attached client.
func (s *Session) broadcastParticipants() {
	s.mu.Lock()
	views := participantViewsLocked(s)
	s.mu.Unlock()

	s.broadcast("participant_update", ParticipantUpdatePayload{Participants: views})
}

// broadcastDeck sends the just-dealt, ordered deck to every attached client.
// Every client receives the exact same ordering.
func (s *Session) broadcastDeck() {
	s.mu.Lock()
	deck := make([]Movie, len(s.Deck))
	copy(deck, s.Deck)
	s.mu.Unlock()

	s.broadcast("deck", DeckPayload{Movies: deck})
}

// broadcastSessionState sends every attached client its own personalized
// session_state snapshot — status/requiredCount/roster are shared, but
// YourParticipantID/YourToken differ per recipient, so this cannot reuse the
// plain broadcast() helper. Used after host:start locks the roster and
// activates the session.
func (s *Session) broadcastSessionState() {
	s.mu.Lock()
	status := s.Status
	code := s.Code
	requiredCount := s.RequiredCount
	participants := participantViewsLocked(s)
	clients := make(map[string]*Client, len(s.clients))
	for id, c := range s.clients {
		clients[id] = c
	}
	tokens := make(map[string]string, len(s.Participants))
	for id, p := range s.Participants {
		tokens[id] = p.Token
	}
	s.mu.Unlock()

	for pid, c := range clients {
		// We don't bother recalculating YourVotes for broadcastSessionState because
		// it only happens at host:start when Votes is empty anyway!
		c.sendJSON("session_state", SessionStatePayload{
			Status:            status,
			Code:              code,
			RequiredCount:     requiredCount,
			Participants:      participants,
			YourParticipantID: pid,
			YourToken:         tokens[pid],
		})
	}
}

// broadcast sends msgType/payload to every client currently attached to the
// session. It reads the client list under the lock, then sends outside it
// so a slow/blocked client can never stall session mutation.
func (s *Session) broadcast(msgType string, payload interface{}) {
	data, err := newEnvelope(msgType, payload)
	if err != nil {
		log.Printf("ws: marshal broadcast %s: %v", msgType, err)
		return
	}

	s.mu.Lock()
	clients := make([]*Client, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	for _, c := range clients {
		c.trySend(data)
	}
}
