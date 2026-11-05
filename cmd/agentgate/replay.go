package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"agentgate/replay"
)

func cmdReplay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	mode := fs.String("mode", "replay", "record | replay | replay-or-record")
	cassette := fs.String("cassette", "tools.jsonl", "cassette file")
	upstream := fs.String("upstream", "", "real tool server (record modes)")
	addr := fs.String("addr", "127.0.0.1:8181", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// An unknown name must not fall through to the zero Mode (Record), which
	// would call the upstream and overwrite the cassette on exit.
	m, ok := map[string]replay.Mode{"record": replay.Record, "replay": replay.Replay, "replay-or-record": replay.ReplayOrRecord}[*mode]
	if !ok {
		return fmt.Errorf("replay: unknown -mode %q (want record, replay or replay-or-record)", *mode)
	}
	if m != replay.Replay && *upstream == "" {
		return fmt.Errorf("replay: -mode %s needs -upstream", *mode)
	}
	c := replay.New(m)
	if m != replay.Record {
		if l, err := replay.Load(*cassette, m); err == nil {
			c = l
		} else if m == replay.Replay {
			return err
		}
	}
	srv := &http.Server{Addr: *addr, Handler: &replay.Proxy{C: c, Upstream: *upstream}, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		<-ch
		if m != replay.Replay {
			if err := c.Save(*cassette); err != nil {
				log.Print(err)
			}
		}
		log.Printf("hits=%d misses=%d", c.Hits, c.Misses)
		_ = srv.Close()
	}()
	log.Printf("tool %s proxy on %s", *mode, *addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
