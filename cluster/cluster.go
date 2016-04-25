package cluster

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

const (
	ConnectionTimeout = 10 * time.Second
)

var respOKMarshalled []byte

func init() {
	var err error
	respOKMarshalled, err = json.Marshal(response{})
	if err != nil {
		panic(fmt.Sprintf("unable to JSON marshal OK response: %s", err.Error()))
	}
}

type response struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Listener is the interface the network service must provide.
type Listener interface {
	net.Listener

	// Dial is used to create a new outgoing connection
	Dial(address string, timeout time.Duration) (net.Conn, error)
}

// Store represents a store of information, managed via consensus.
type Store interface {
	// Leader returns the leader of the consensus system.
	Leader() string

	// UpdateAPIPeers updates the API peers on the store.
	UpdateAPIPeers(peers map[string]string) error
}

// Service allows access to the cluster and associated meta data,
// via consensus.
type Service struct {
	ln    Listener
	store Store
	addr  net.Addr

	wg sync.WaitGroup

	logger *log.Logger
}

// NewService returns a new instance of the cluster service
func NewService(ln Listener, store Store) *Service {
	return &Service{
		ln:     ln,
		store:  store,
		addr:   ln.Addr(),
		logger: log.New(os.Stderr, "[cluster] ", log.LstdFlags),
	}
}

// Open opens the Service.
func (s *Service) Open() error {
	s.wg.Add(1)
	go s.serve()
	s.logger.Println("service listening on", s.ln.Addr())
	return nil
}

// Close closes the service.
func (s *Service) Close() error {
	s.ln.Close()
	s.wg.Wait()
	return nil
}

// Addr returns the address the service is listening on.
func (s *Service) Addr() string {
	return s.addr.String()
}

// SetPeer will set the mapping between raftAddr and apiAddr for the entire cluster.
func (s *Service) SetPeer(raftAddr, apiAddr string) error {
	peer := map[string]string{
		raftAddr: apiAddr,
	}

	// Try the local store. It might be the leader.
	err := s.store.UpdateAPIPeers(peer)
	if err == nil {
		// All done! Aren't we lucky?
		return nil
	}

	// Try talking to the leader over the network.
	conn, err := s.ln.Dial(s.store.Leader(), ConnectionTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	b, err := json.Marshal(peer)
	if err != nil {
		return err
	}

	if _, err := conn.Write(b); err != nil {
		return err
	}

	// Wait for the response and verify the operation went through.
	resp := response{}
	d := json.NewDecoder(conn)
	err = d.Decode(&resp)
	if err != nil {
		return err
	}

	if resp.Code != 0 {
		return fmt.Errorf(resp.Message)
	}
	return nil
}

func (s *Service) serve() error {
	defer s.wg.Done()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return err
		}

		go s.handleConn(conn)
	}
}

func (s *Service) handleConn(conn net.Conn) {
	// Only handles peers updates for now.
	peers := make(map[string]string)
	d := json.NewDecoder(conn)
	err := d.Decode(&peers)
	if err != nil {
		return
	}

	// Update the peers.
	if err := s.store.UpdateAPIPeers(peers); err != nil {
		resp := response{1, err.Error()}
		b, err := json.Marshal(resp)
		if err != nil {
			conn.Close() // Only way left to signal.
		} else {
			if _, err := conn.Write(b); err != nil {
				conn.Close() // Only way left to signal.
			}
		}
		return
	}

	// Let the remote node know everything went OK.
	if _, err := conn.Write(respOKMarshalled); err != nil {
		conn.Close() // Only way left to signal.
	}
	return
}