package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Proxy exposes a cassette as an HTTP tool server: POST /tools/{name} with
// the arguments as the JSON body. In record modes it forwards misses to
// Upstream + /tools/{name}. An agent harness pointed at the proxy instead of
// its real tool server becomes replayable without code changes.
type Proxy struct {
	C        *Cassette
	Upstream string
	Client   *http.Client
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/tools/")
	if !ok || r.Method != http.MethodPost || name == "" {
		http.Error(w, "POST /tools/{name}", http.StatusNotFound)
		return
	}
	args, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = []byte("{}")
	}
	var up func() (json.RawMessage, error)
	if p.Upstream != "" {
		up = func() (json.RawMessage, error) {
			cl := p.Client
			if cl == nil {
				cl = &http.Client{Timeout: 60 * time.Second}
			}
			resp, err := cl.Post(strings.TrimRight(p.Upstream, "/")+"/tools/"+name, "application/json", bytes.NewReader(args))
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode >= 300 {
				return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, b)
			}
			return b, nil
		}
	}
	res, err := p.C.Call(name, args, up)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(res)
}
