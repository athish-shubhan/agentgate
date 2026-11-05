// Package replay records tool calls and plays them back ("VCR for tools").
//
// When only the grader or the gate logic changes, there is no reason to call
// real tools (or the model) again: a cassette recorded once makes the run
// deterministic and free. Identical calls (same tool, same arguments after
// canonical JSON encoding) are kept in order, so the second identical call
// gets the second recorded result: state that changed between the two calls
// is reproduced, not collapsed.
package replay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Mode int

const (
	Record         Mode = iota // call upstream and append to the cassette
	Replay                     // serve from the cassette; a miss is an error
	ReplayOrRecord             // serve if recorded, otherwise call upstream and record
)

type Entry struct {
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args"`
	Result json.RawMessage `json:"result,omitempty"`
	// Text holds an upstream body that is not JSON. Stored in Result it made
	// every later Save fail (json.RawMessage must be valid JSON).
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// body is what a replayed call returns: the bytes the upstream sent.
func (e Entry) body() json.RawMessage {
	if e.Text != "" {
		return json.RawMessage(e.Text)
	}
	return e.Result
}

var (
	ErrMiss    = errors.New("replay: call not in cassette")
	ErrBadArgs = errors.New("replay: tool arguments are not valid JSON")
)

type Cassette struct {
	mu       sync.Mutex
	mode     Mode
	entries  map[string][]Entry
	next     map[string]int
	recorded []Entry
	Hits     int
	Misses   int
}

func New(mode Mode) *Cassette {
	return &Cassette{mode: mode, entries: map[string][]Entry{}, next: map[string]int{}}
}

// Load reads a cassette written by Save (JSON lines).
func Load(path string, mode Mode) (*Cassette, error) {
	c := New(mode)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		k := key(e.Tool, e.Args)
		c.entries[k] = append(c.entries[k], e)
		c.recorded = append(c.recorded, e)
	}
	return c, sc.Err()
}

// Save writes the cassette atomically: to a temporary file next to path,
// then renamed over it. It used to truncate path first, so an error while
// writing (a full disk, an unencodable entry) left an empty cassette and
// every recorded call was lost.
func (c *Cassette) Save(path string) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range c.recorded {
		if err = enc.Encode(e); err != nil {
			return err
		}
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Canonical re-encodes JSON with sorted keys and no insignificant whitespace.
func Canonical(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, _ := json.Marshal(v)
	return b
}

func key(tool string, args json.RawMessage) string { return tool + "\x00" + string(Canonical(args)) }

// Call serves a tool call from the cassette or from upstream, per the mode.
func (c *Cassette) Call(tool string, args json.RawMessage, upstream func() (json.RawMessage, error)) (json.RawMessage, error) {
	if !json.Valid(args) {
		// it could be neither matched canonically nor saved
		return nil, fmt.Errorf("%w: %s", ErrBadArgs, tool)
	}
	k := key(tool, args)
	c.mu.Lock()
	if c.mode != Record {
		if es := c.entries[k]; c.next[k] < len(es) {
			e := es[c.next[k]]
			c.next[k]++
			c.Hits++
			c.mu.Unlock()
			if e.Error != "" {
				return nil, errors.New(e.Error)
			}
			return e.body(), nil
		}
		c.Misses++
		if c.mode == Replay {
			c.mu.Unlock()
			return nil, fmt.Errorf("%w: %s %s", ErrMiss, tool, Canonical(args))
		}
	}
	c.mu.Unlock()
	if upstream == nil {
		return nil, fmt.Errorf("%w and no upstream: %s", ErrMiss, tool)
	}
	res, err := upstream()
	e := Entry{Tool: tool, Args: Canonical(args)}
	if json.Valid(res) {
		e.Result = res
	} else {
		e.Text = string(res)
	}
	if err != nil {
		e.Error = err.Error()
	}
	c.mu.Lock()
	c.recorded = append(c.recorded, e)
	c.entries[k] = append(c.entries[k], e)
	c.next[k] = len(c.entries[k])
	c.mu.Unlock()
	return res, err
}
