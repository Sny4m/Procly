package session

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/procly/procly/internal/logs"
	"github.com/procly/procly/internal/process"
	"github.com/procly/procly/internal/storage"
	"io"
	"sync"
	"time"
)

type Request struct {
	Action, Name, Stream, Search string
	After                        uint64
}
type Response struct {
	Records []process.Record  `json:"records,omitempty"`
	Events  []logs.Event      `json:"events,omitempty"`
	Members map[string]string `json:"members,omitempty"`
	Error   string            `json:"error,omitempty"`
}

func Serve(c *Controller) (func(), error) {
	listener, e := listen(c.Store)
	if e != nil {
		return nil, e
	}
	done := make(chan struct{})
	var handlers sync.WaitGroup
	slots := make(chan struct{}, 16)
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				handlers.Add(1)
				go func() {
					defer handlers.Done()
					defer func() { <-slots }()
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(75 * time.Second))
					var req Request
					if e := json.NewDecoder(io.LimitReader(conn, 65536)).Decode(&req); e != nil {
						return
					}
					res := c.handle(req)
					_ = json.NewEncoder(conn).Encode(res)
				}()
			default:
				conn.Close()
			}
		}
	}()
	return func() { listener.Close(); <-done; handlers.Wait() }, nil
}
func (c *Controller) handle(req Request) Response {
	var r Response
	var e error
	switch req.Action {
	case "ps":
		r.Records = c.Manager.Snapshots()
	case "stop":
		e = c.Stop(req.Name)
	case "restart":
		e = c.Restart(req.Name)
	case "start":
		_, e = c.Start(req.Name, process.Output{})
	case "logs":
		r.Events = c.Logs.Read(req.Name, req.Stream, req.Search, req.After)
	case "group-start":
		r.Members = c.Group(req.Name, false)
	case "group-stop":
		r.Members = c.Group(req.Name, true)
	default:
		e = errors.New("unsupported session operation")
	}
	if e != nil {
		r.Error = e.Error()
	}
	return r
}
func Call(ctx context.Context, s *storage.Store, req Request) (Response, error) {
	var response Response
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	conn, e := dial(ctx, s)
	if e != nil {
		return response, errors.New("no active Procly session; open procly first")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if e = json.NewEncoder(conn).Encode(req); e != nil {
		return response, e
	}
	e = json.NewDecoder(io.LimitReader(conn, 16*1024*1024)).Decode(&response)
	if e != nil {
		return response, e
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}
