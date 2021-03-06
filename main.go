package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Request struct {
	Method     string      `json:"method"`
	Header     http.Header `json:"header"`
	RemoteAddr string      `json:"remote_addr"`
	RequestURI string      `json:"uri"`
	Body       []byte      `json:"body"`
	RequestID  uuid.UUID   `json:"request_id"`
}

type Response struct {
	Body      []byte    `json:"body"`
	RequestID uuid.UUID `json:"request_id"`
}

type ResponseWaiters struct {
	waiters map[uuid.UUID]chan *Response
	sync.Mutex
}

func NewResponseWaiters() *ResponseWaiters {
	return &ResponseWaiters{waiters: make(map[uuid.UUID]chan *Response)}
}

func (r *ResponseWaiters) Get(request_id uuid.UUID) *Response {
	c := make(chan *Response)
	r.Lock()
	r.waiters[request_id] = c
	r.Unlock()
	return <-c
}

func (r *ResponseWaiters) Respond(request_id uuid.UUID, response *Response) {
	r.Lock()
	defer r.Unlock()
	if c, ok := r.waiters[request_id]; ok {
		delete(r.waiters, request_id)
		c <- response
		return
	}
	emitPacket("http.response.log", &ResponseLog{
		Err:      "unknown request",
		Response: response,
	})
}

type ResponseLog struct {
	Err      string    `json:"error,omitempty"`
	Took     float64   `json:"took,omitempty"`
	Response *Response `json:"response,omitempty"`
	Raw      []byte    `json:"raw,omitempty"`
}

type Packet struct {
	App     string      `json:"app"`
	Content interface{} `json:"content"`
}

func emitPacket(app string, content interface{}) {
	packet := &Packet{App: app, Content: content}
	json.NewEncoder(os.Stdout).Encode(packet)
}

func main() {
	responses := NewResponseWaiters()

	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			response := new(Response)
			err := json.Unmarshal(scanner.Bytes(), response)
			if err != nil {
				emitPacket("http.response.log", &ResponseLog{
					Err: fmt.Sprintf("malformed: %s ", err),
					Raw: scanner.Bytes(),
				})
				continue
			}
			responses.Respond(response.RequestID, response)
		}
		if err := scanner.Err(); err != nil {
			panic(err)
		}
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		start := time.Now()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			panic(err)
		}

		requestID := uuid.New()
		req := &Request{
			Method:     r.Method,
			Header:     r.Header,
			RemoteAddr: r.RemoteAddr,
			RequestURI: r.RequestURI,
			Body:       body,
			RequestID:  requestID,
		}

		// there's an almost impossible race condition here. if a responder can
		// write to STDIN fast enough so a response is received before
		// `responses.Get` is called, the response will be thrown away as an
		// "unknown request"
		emitPacket("http.request", req)
		response := responses.Get(requestID)

		w.Write(response.Body)

		took := math.Round(float64(time.Since(start))/float64(time.Millisecond)*10) / 10
		emitPacket("http.response.log", &ResponseLog{
			Response: response,
			Took:     took,
		})
	})

	log.Fatal(http.ListenAndServe(":8080", nil))
}
