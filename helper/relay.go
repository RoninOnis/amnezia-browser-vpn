package main

// Релей AmneziaWG через 443/udp.
//
// Зачем: в корпоративных сетях наружу часто разрешён UDP только на 443 (QUIC),
// а остальные порты режет фаервол — см. живой замер: udp/443 OK, udp/37690 BLOCKED.
// AmneziaWG работает по UDP и на 443 не переедет, если сервер чужой.
//
// Решение: наш сервис слушает 443/udp, а пакеты пересылает на настоящий сервер
// клиента с его штатного порта. AmneziaWG проверяет пира по ключам, а не по адресу
// источника, и запоминает адрес, с которого пришёл валидный handshake (штатный
// роуминг WireGuard) — поэтому сервер клиента сам начинает отвечать релею, и на
// сервере ничего менять не нужно.
//
// Чтобы релей не стал открытым прокси для кого угодно, в нём два ограничителя:
// общий токен и белый список адресов, куда разрешено пересылать.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	relayMagic       = "AWGBVR1\n" // 8 байт
	relaySessionIdle = 120 * time.Second
	relayMaxPayload  = 2048
)

// Коды ответа релея.
const (
	relayOK         = 0
	relayBadToken   = 1
	relayNotAllowed = 2
	relayBadRequest = 3
	relayServerErr  = 4
)

type relayRequest struct {
	Token   string
	Target  string // "ip:port" — адрес настоящего сервера AmneziaWG
	Payload []byte
}

func encodeRelayRequest(r relayRequest) []byte {
	buf := make([]byte, 0, 8+1+len(r.Token)+2+len(r.Target)+len(r.Payload))
	buf = append(buf, relayMagic...)
	buf = append(buf, byte(len(r.Token)))
	buf = append(buf, r.Token...)
	var port [4]byte
	binary.BigEndian.PutUint32(port[:], uint32(len(r.Target)))
	buf = append(buf, port[:]...)
	buf = append(buf, r.Target...)
	buf = append(buf, r.Payload...)
	return buf
}

func decodeRelayRequest(b []byte) (relayRequest, error) {
	var out relayRequest
	if len(b) < len(relayMagic)+1 {
		return out, errors.New("relay: короткий пакет")
	}
	if string(b[:len(relayMagic)]) != relayMagic {
		return out, errors.New("relay: не наш заголовок")
	}
	p := len(relayMagic)
	tokLen := int(b[p])
	p++
	if len(b) < p+tokLen+4 {
		return out, errors.New("relay: обрезанный токен")
	}
	out.Token = string(b[p : p+tokLen])
	p += tokLen
	tgtLen := int(binary.BigEndian.Uint32(b[p : p+4]))
	p += 4
	if tgtLen <= 0 || len(b) < p+tgtLen {
		return out, errors.New("relay: обрезанный адрес")
	}
	out.Target = string(b[p : p+tgtLen])
	p += tgtLen
	out.Payload = b[p:]
	return out, nil
}

func encodeRelayReply(code byte, payload []byte) []byte {
	buf := make([]byte, 0, len(relayMagic)+1+len(payload))
	buf = append(buf, relayMagic...)
	buf = append(buf, code)
	buf = append(buf, payload...)
	return buf
}

func decodeRelayReply(b []byte) (byte, []byte, error) {
	if len(b) < len(relayMagic)+1 || string(b[:len(relayMagic)]) != relayMagic {
		return 0, nil, errors.New("relay: чужой ответ")
	}
	return b[len(relayMagic)], b[len(relayMagic)+1:], nil
}

// ---------------------------------------------------------------- сервер

// RelayServer пересылает UDP-пакеты клиентов на разрешённые адреса.
type RelayServer struct {
	Addr  string
	Token string
	// FixedTarget — режим «спутник сервера»: релей стоит на том же хосте, что и
	// сервер AmneziaWG, игнорирует запрошенный клиентом адрес и всегда пересылает
	// в эту локальную цель (например 127.0.0.1:36853). Так не нужны ни правка
	// контейнера, ни новые ссылки vpn://, ни настройки у клиента: клиент просто
	// попадает на 443/udp, а релей отдаёт пакеты локальному AWG. Токен в этом
	// режиме можно не задавать: цель фиксирована, а без ключей AWG всё равно
	// ничего не сделаешь.
	FixedTarget string
	Allowed     map[string]bool // "ip:port": куда разрешено пересылать

	conn      *net.UDPConn
	mu        sync.Mutex
	sessions  map[string]*relaySession
	closing   chan struct{}
	closeOnce sync.Once
}

type relaySession struct {
	up     *net.UDPConn
	client *net.UDPAddr
	last   time.Time
	// wrapped: клиент говорит на нашем протоколе (релей как отдельный сервис) —
	// ответы уходят с заголовком. Иначе режим прозрачный: клиент просто шлёт
	// AWG-пакеты на 443, как будто это сам сервер, и получает их как есть.
	wrapped bool
}

func (s *RelayServer) Start() error {
	conn, err := net.ListenUDP("udp", mustResolveUDP(s.Addr))
	if err != nil {
		return fmt.Errorf("relay listen %s: %w", s.Addr, err)
	}
	s.conn = conn
	s.sessions = map[string]*relaySession{}
	s.closing = make(chan struct{})
	if s.FixedTarget != "" {
		log.Printf("relay listening on %s, fixed target %s (спутник сервера)", conn.LocalAddr(), s.FixedTarget)
	} else {
		log.Printf("relay listening on %s, allowed targets: %d", conn.LocalAddr(), len(s.Allowed))
	}
	go s.readLoop()
	go s.gcLoop()
	return nil
}

func (s *RelayServer) readLoop() {
	buf := make([]byte, relayMaxPayload)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.closing:
				return
			default:
				log.Printf("relay: read: %v", err)
				return
			}
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		s.handle(pkt, addr)
	}
}

func (s *RelayServer) handle(pkt []byte, addr *net.UDPAddr) {
	key := addr.String()
	if strings.HasPrefix(string(pkt), relayMagic) {
		req, err := decodeRelayRequest(pkt)
		if err != nil {
			s.reply(addr, relayBadRequest, nil)
			return
		}
		if s.Token != "" && req.Token != s.Token {
			log.Printf("relay: отказано клиенту %s: неверный токен", key)
			s.reply(addr, relayBadToken, nil)
			return
		}
		target := req.Target
		if s.FixedTarget != "" {
			target = s.FixedTarget
		} else if !s.allowed(req.Target) {
			log.Printf("relay: отказано клиенту %s: адрес %q не в белом списке", key, req.Target)
			s.reply(addr, relayNotAllowed, nil)
			return
		}
		sess, err := s.sessionFor(key, addr, target)
		if err != nil {
			log.Printf("relay: сессия для %s: %v", key, err)
			s.reply(addr, relayServerErr, nil)
			return
		}
		s.reply(addr, relayOK, nil)
		if len(req.Payload) > 0 {
			if _, err := sess.up.Write(req.Payload); err != nil {
				log.Printf("relay: пересылка в %s: %v", target, err)
			}
		}
		return
	}

	s.mu.Lock()
	sess := s.sessions[key]
	s.mu.Unlock()
	if sess == nil {
		if s.FixedTarget == "" {
			return // чужой пакет без сессии: в обычном режиме игнорируем
		}
		// прозрачный режим: клиент не знает про релей, создаём сессию по факту пакета.
		// wrapped=false сразу: иначе в логе сессия помечается «протокол», хотя клиент
		// говорит обычными AWG-пакетами, и разбор инцидента идёт по ложному следу.
		var err error
		sess, err = s.newSession(key, addr, s.FixedTarget, false)
		if err != nil {
			log.Printf("relay: прозрачная сессия для %s: %v", key, err)
			return
		}
	}
	sess.last = time.Now()
	if _, err := sess.up.Write(pkt); err != nil {
		log.Printf("relay: пересылка: %v", err)
	}
}

func (s *RelayServer) allowed(target string) bool {
	if len(s.Allowed) == 0 {
		return false
	}
	return s.Allowed[target]
}

func (s *RelayServer) sessionFor(key string, addr *net.UDPAddr, target string) (*relaySession, error) {
	return s.newSession(key, addr, target, true)
}

func (s *RelayServer) newSession(key string, addr *net.UDPAddr, target string, wrapped bool) (*relaySession, error) {
	s.mu.Lock()
	if sess, ok := s.sessions[key]; ok {
		sess.last = time.Now()
		s.mu.Unlock()
		return sess, nil
	}
	s.mu.Unlock()

	up, err := net.DialUDP("udp", nil, mustResolveUDP(target))
	if err != nil {
		return nil, err
	}
	sess := &relaySession{up: up, client: addr, last: time.Now(), wrapped: wrapped}
	s.mu.Lock()
	s.sessions[key] = sess
	s.mu.Unlock()

	go func() {
		buf := make([]byte, relayMaxPayload)
		for {
			n, err := up.Read(buf)
			if err != nil {
				return
			}
			sess.last = time.Now()
			out := buf[:n]
			if sess.wrapped {
				// тем же заголовком, что и служебные ответы: клиент разбирает единообразно
				out = encodeRelayReply(relayOK, buf[:n])
			}
			if _, err := s.conn.WriteToUDP(out, addr); err != nil {
				return
			}
		}
	}()
	mode := "протокол"
	if !wrapped {
		mode = "прозрачно"
	}
	log.Printf("relay: сессия %s -> %s (%s)", key, target, mode)
	return sess, nil
}

func (s *RelayServer) reply(addr *net.UDPAddr, code byte, payload []byte) {
	if _, err := s.conn.WriteToUDP(encodeRelayReply(code, payload), addr); err != nil {
		log.Printf("relay: ответ %s: %v", addr, err)
	}
}

func (s *RelayServer) gcLoop() {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.closing:
			return
		case <-tick.C:
			now := time.Now()
			s.mu.Lock()
			for k, sess := range s.sessions {
				if now.Sub(sess.last) > relaySessionIdle {
					sess.up.Close()
					delete(s.sessions, k)
					log.Printf("relay: сессия %s закрыта по простою", k)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *RelayServer) Close() {
	s.closeOnce.Do(func() {
		close(s.closing)
		if s.conn != nil {
			s.conn.Close()
		}
		s.mu.Lock()
		for k, sess := range s.sessions {
			sess.up.Close()
			delete(s.sessions, k)
		}
		s.mu.Unlock()
	})
}

// ---------------------------------------------------------------- клиент

// RelayClient поднимает локальный UDP-порт, в который смотрит AmneziaWG-девайс,
// и пересылает пакеты через релей на настоящий сервер.
type RelayClient struct {
	conn      *net.UDPConn // локальный, для девайса
	up        *net.UDPConn // к релею
	target    string
	token     string
	localAddr string

	mu          sync.Mutex
	devAddr     *net.UDPAddr
	established bool

	closing   chan struct{}
	closeOnce sync.Once
}

// StartRelayClient(relayAddr, token, target) — relayAddr это адрес нашего релея,
// target — настоящий сервер AmneziaWG ("ip:port") из профиля.
func StartRelayClient(relayAddr, token, target string) (*RelayClient, error) {
	conn, err := net.ListenUDP("udp", mustResolveUDP("127.0.0.1:0"))
	if err != nil {
		return nil, fmt.Errorf("relay client listen: %w", err)
	}
	up, err := net.DialUDP("udp", nil, mustResolveUDP(relayAddr))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("relay client dial %s: %w", relayAddr, err)
	}
	c := &RelayClient{
		conn:      conn,
		up:        up,
		target:    target,
		token:     token,
		localAddr: conn.LocalAddr().String(),
		closing:   make(chan struct{}),
	}
	go c.deviceLoop()
	go c.relayLoop()
	go c.keepaliveLoop()
	log.Printf("relay client: 127.0.0.1:%d -> %s -> %s", conn.LocalAddr().(*net.UDPAddr).Port, relayAddr, target)
	return c, nil
}

func (c *RelayClient) LocalAddr() string { return c.localAddr }

func (c *RelayClient) Established() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.established
}

func (c *RelayClient) preamble() []byte {
	return encodeRelayRequest(relayRequest{Token: c.token, Target: c.target})
}

func (c *RelayClient) deviceLoop() {
	buf := make([]byte, relayMaxPayload)
	for {
		n, addr, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-c.closing:
				return
			default:
				log.Printf("relay client: read: %v", err)
				return
			}
		}
		c.mu.Lock()
		c.devAddr = addr
		est := c.established
		c.mu.Unlock()

		if est {
			c.up.Write(buf[:n])
		} else {
			// первый пакет склеиваем с запросом на сессию, чтобы не терять handshake
			req := encodeRelayRequest(relayRequest{Token: c.token, Target: c.target, Payload: buf[:n]})
			c.up.Write(req)
		}
	}
}

func (c *RelayClient) relayLoop() {
	buf := make([]byte, relayMaxPayload)
	for {
		n, err := c.up.Read(buf)
		if err != nil {
			select {
			case <-c.closing:
				return
			default:
				log.Printf("relay client: релей закрыл соединение: %v", err)
				return
			}
		}
		code, payload, err := decodeRelayReply(buf[:n])
		if err != nil {
			continue
		}
		switch code {
		case relayOK:
			c.mu.Lock()
			if !c.established {
				c.established = true
			}
			dev := c.devAddr
			c.mu.Unlock()
			if len(payload) > 0 && dev != nil {
				c.conn.WriteToUDP(payload, dev)
			}
		case relayBadToken:
			log.Printf("relay client: релей отклонил токен")
		case relayNotAllowed:
			log.Printf("relay client: релей не разрешает адрес %s", c.target)
		case relayServerErr:
			log.Printf("relay client: релей не смог открыть сессию к %s", c.target)
		}
	}
}

// keepaliveLoop переспрашивает сессию, пока она не установлена: тогда подключение
// восстанавливается само, если релей перезапустился.
func (c *RelayClient) keepaliveLoop() {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-c.closing:
			return
		case <-tick.C:
			c.mu.Lock()
			est := c.established
			c.mu.Unlock()
			if !est {
				c.up.Write(c.preamble())
			}
		}
	}
}

func (c *RelayClient) Close() {
	c.closeOnce.Do(func() {
		close(c.closing)
		c.conn.Close()
		c.up.Close()
	})
}

func mustResolveUDP(addr string) *net.UDPAddr {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return &net.UDPAddr{}
	}
	return a
}
