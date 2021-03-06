package pelicantun

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"sync"
	"time"

	// _ "net/http/pprof" // side-effect: installs handlers for /debug/pprof
)

type WebServer struct {
	ServerReady chan bool // closed once server is listening on Addr
	Done        chan bool // closed when server shutdown.

	requestStop chan bool // private. Users should call Stop().

	// we use tigertonic-based web-server because it implements graceful stopping;
	// as opposed to the built-in http library web server.
	tts *CustomHttpServer

	started bool
	stopped bool
	Cfg     WebServerConfig

	Name string // distinguish for debug prints the various web server uses/at Start()
	mut  sync.Mutex
}

type WebServerConfig struct {
	Listen      Addr
	ReadTimeout time.Duration
}

func NewWebServer(cfg WebServerConfig, mux *http.ServeMux) (*WebServer, error) {

	if mux == nil {
		mux = http.NewServeMux()
	}

	// get an available port
	if cfg.Listen.Port == 0 {
		cfg.Listen.Port = GetAvailPort()
	}
	if cfg.Listen.Ip == "" {
		cfg.Listen.Ip = "0.0.0.0"
	}
	cfg.Listen.SetIpPort()
	//VPrintf("hey hey: starting webserver on '%s'\n", cfg.Listen.IpPort)

	// check that it isn't already occupied
	if PortIsBound(cfg.Listen.IpPort) {
		return nil, fmt.Errorf("NewWebServer error: could not start because port already in-use on '%s'", cfg.Listen.IpPort)
	}

	s := &WebServer{
		Cfg:         cfg,
		ServerReady: make(chan bool),
		Done:        make(chan bool),
		requestStop: make(chan bool),
	}

	s.tts = NewCustomHttpServer(s.Cfg.Listen.IpPort, mux, s.Cfg.ReadTimeout)
	//s.tts = NewCustomHttpServer(s.Cfg.Addr, http.DefaultServeMux) // supply debug/pprof diagnostics

	return s, nil
}

func (s *WebServer) Start(webName string) {
	s.Name = webName
	if s.started {
		return
	}
	s.started = true
	po("WebServer::Start('%s') begun, for s = %p.  Listen: %v", webName, s, s.Cfg.Listen.IpPort)

	go func() {
		err := s.tts.ListenAndServe()
		if nil != err {
			po("WebServer::Start('%s') done with s.tts.ListenAndServer(); err = '%s'.\n", webName, err)
			//log.Println(err) // accept tcp 127.0.0.1:3000: use of closed network connection
		}
		s.stopped = true
		close(s.Done)
		po("WebServer::Start() inner goroutine done, for s = %p.\n", s)
	}()

	WaitUntilServerUp(s.Cfg.Listen.IpPort)
	close(s.ServerReady)
}

func (s *WebServer) Stop() {
	if s.stopped || s.IsStopRequested() {
		// not stopping races here, just preventing
		// panic on two serial Stops() under web_test.go failure situation.
		return
	}
	VPrintf("in WebServer::Stop() about to request stop ... s = %p\n", s)
	weClosed := s.RequestStop()
	if weClosed {
		s.tts.Close() // without weClosed check, hang here because tts is already down and gone.
	}
	VPrintf("in WebServer::Stop() after s.tts.Close() ... s = %p\n", s)
	<-s.Done
	VPrintf("in WebServer::Stop() after <-s.Done(): s.Addr = '%s' ... s = %p\n", s.Cfg.Listen.IpPort, s)

	WaitUntilServerDown(s.Cfg.Listen.IpPort)
}

func (s *WebServer) IsStopRequested() bool {
	s.mut.Lock()
	defer s.mut.Unlock()
	select {
	case <-s.requestStop:
		return true
	default:
		return false
	}
}

// RequestStop makes sure we only close
// the s.requestStop channel once. Returns
// true iff we closed s.requestStop on this call.
func (s *WebServer) RequestStop() bool {
	s.mut.Lock()
	defer s.mut.Unlock()

	select {
	case <-s.requestStop:
		return false
	default:
		close(s.requestStop)
		return true
	}
}

func WaitUntilServerUp(addr string) {
	attempt := 1
	for {
		if PortIsBound(addr) {
			return
		}
		time.Sleep(500 * time.Millisecond)
		attempt++
		if attempt > 40 {
			panic(fmt.Sprintf("could not connect to server at '%s' after 40 tries of 500msec", addr))
		}
	}
}

func WaitUntilServerDown(addr string) {
	attempt := 1
	for {
		if !PortIsBound(addr) {
			return
		}
		//fmt.Printf("WaitUntilServerUp: on attempt %d, sleep then try again\n", attempt)
		time.Sleep(500 * time.Millisecond)
		attempt++
		if attempt > 40 {
			panic(fmt.Sprintf("could always connect to server at '%s' after 40 tries of 500msec", addr))
		}
	}
}

func PortIsBound(addr string) bool {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return false
	}
	VPrintf("\n\n PortIsBound opened as client %v -> %v\n\n", conn.LocalAddr(), conn.RemoteAddr())
	conn.Close()
	return true
}

func FetchUrl(url string) ([]byte, error) {
	response, err := http.Get(url)

	defer func() {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
	}()
	if err != nil {
		return []byte{}, err
	} else {
		contents, err := ioutil.ReadAll(response.Body)
		if err != nil {
			return []byte{}, err
		}
		return contents, nil
	}
}

// mock for http.ResponseWriter

func NewMockResponseWriter() *MockResponseWriter {
	return &MockResponseWriter{
		header: make(http.Header),
	}
}

type MockResponseWriter struct {
	store   bytes.Buffer
	header  http.Header
	errcode int
}

// Header returns the header map that will be sent by WriteHeader.
// Changing the header after a call to WriteHeader (or Write) has
// no effect.
func (m *MockResponseWriter) Header() http.Header {
	if m.header == nil {
		m.header = make(http.Header)
	}
	return m.header
}

// Write writes the data to the connection as part of an HTTP reply.
// If WriteHeader has not yet been called, Write calls WriteHeader(http.StatusOK)
// before writing the data.  If the Header does not contain a
// Content-Type line, Write adds a Content-Type set to the result of passing
// the initial 512 bytes of written data to DetectContentType.
func (m *MockResponseWriter) Write(p []byte) (int, error) {
	return m.store.Write(p)
}

// WriteHeader sends an HTTP response header with status code.
// If WriteHeader is not called explicitly, the first call to Write
// will trigger an implicit WriteHeader(http.StatusOK).
// Thus explicit calls to WriteHeader are mainly used to
// send error codes.
func (m *MockResponseWriter) WriteHeader(status int) {
	m.errcode = status
}
