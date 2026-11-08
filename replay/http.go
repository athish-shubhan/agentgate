package replay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Proxy exposes a cassette as an HTTP tool server: POST /tools/{name} with
// the arguments as the JSON body. In record modes it forwards misses to
// Upstream + /tools/{name}. An agent harness pointed at the proxy instead of
// its real tool server becomes replayable without code changes.
// maxBody caps request and upstream response sizes.
const maxBody = 16 << 20

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
	if strings.Contains(name, "/") || name == "." || name == ".." {
		http.Error(w, "invalid tool name", http.StatusBadRequest)
		return
	}
	args, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		code := http.StatusBadRequest
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			code = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), code)
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
			resp, err := cl.Post(strings.TrimRight(p.Upstream, "/")+"/tools/"+url.PathEscape(name), "application/json", bytes.NewReader(args))
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
			if err != nil {
				return nil, err
			}
			if len(b) > maxBody {
				return nil, fmt.Errorf("upstream response larger than %d bytes", maxBody)
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
