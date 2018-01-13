package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/textproto"
	"os"
	"strconv"
	"strings"

	"./config"
	"./pool"
	"golang.org/x/crypto/bcrypt"
	//"bytes"
	"time"

	"github.com/coreos/go-systemd/daemon"
	"github.com/go-redis/redis"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

var (
	cfg                config.Configuration
	backendConnections map[string]int
	connectionPool     pool.Pool
	db                 *sqlx.DB
	cache              *redis.Client
)

type session struct {
	UserConnection       textproto.Conn
	backendConnection    net.Conn
	command              string
	selectedBackend      *config.SelectedBackend
	User                 User
	Session              Session
	Bytes                int64
	RequestsSinceUpdaate int
}

// Utils
func HashPassword(password string) string {
	bytes, _ := bcrypt.GenerateFromPassword([]byte(password), 10)
	return string(bytes)
}

func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func isCommandAllowed(command string) bool {
	for _, elem := range cfg.Frontend.FrontendAllowedCommands {
		if strings.ToLower(elem.FrontendCommand) == strings.ToLower(command) {
			return true
		}
	}
	return false
}

func LoadConfig(path string) config.Configuration {
	file, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatal("Config File Missing. ", err)
	}

	var configType config.Configuration
	err = json.Unmarshal(file, &configType)
	if err != nil {
		log.Fatal("Config Parse Error: ", err)
	}

	return configType
}

func main() {

	cfg = LoadConfig("config.json")

	db, _ = sqlx.Connect("mysql", "root:@(localhost:3306)/betanews?parseTime=true")
	db.MustExec("UPDATE NewsUser SET connused=0")
	/*if err != nil {
	        log.Fatalln(err)
		}*/

	//db.MustExec(schema)

	cache = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	pong, err := cache.Ping().Result()
	fmt.Println(pong, err)

	backendConnections = make(map[string]int)

	factory := func() (*textproto.Conn, error) { return connectBackend() }

	// move max connections to config
	connectionPool, _ = pool.NewChannelPool(0, 50, factory)

	for _, elem := range cfg.Backend {
		backendConnections[elem.BackendName] = 0
	}

	var l net.Listener

	if cfg.Frontend.FrontendTLS {

		// New var for error
		var err error

		// try to load cert pair
		cer, err := tls.LoadX509KeyPair(cfg.Frontend.FrontendTLSCert, cfg.Frontend.FrontendTLSKey)

		if err != nil {
			log.Printf("%v", err)
			return
		}

		// Set certs
		tlsConf := &tls.Config{Certificates: []tls.Certificate{cer}}

		// Listen for incoming TLS connections.
		l, err = tls.Listen("tcp", cfg.Frontend.FrontendAddr+":"+cfg.Frontend.FrontendPort, tlsConf)

		if err != nil {
			log.Printf("%v", err)
			os.Exit(1)
		}

		log.Printf("[TLS] Listening on %v:%v", cfg.Frontend.FrontendAddr, cfg.Frontend.FrontendPort)

	} else {

		// New var for error
		var err error

		// Listen for incoming connections.
		l, err = net.Listen("tcp", cfg.Frontend.FrontendAddr+":"+cfg.Frontend.FrontendPort)

		if err != nil {
			log.Printf("%v", err)
			os.Exit(1)
		}

		log.Printf("[PLAIN - DO NOT USE PROD!] Listening on %v:%v", cfg.Frontend.FrontendAddr, cfg.Frontend.FrontendPort)
	}

	daemon.SdNotify(false, "READY=1")

	go func() {
		interval, err := daemon.SdWatchdogEnabled(false)
		if err != nil || interval == 0 {
			return
		}
		for {
			daemon.SdNotify(false, "WATCHDOG=1")
			time.Sleep(interval / 3)
		}
	}()

	// Close the listener when the application closes.
	defer l.Close()

	for {
		// Listen for an incoming connection.
		conn, err := l.Accept()
		if err != nil {
			fmt.Println("Error accepting: ", err.Error())
			//os.Exit(1)
		}
		// Handle connections in a new goroutine.
		go handleRequest(conn)
	}
}

func (s *session) dispatchCommand() {

	//log.Printf("[Dispatch] Command : %v", s.command)

	cmd := strings.Split(s.command, " ")

	args := []string{}

	if len(cmd) > 1 {
		args = cmd[1:]
	}

	if strings.ToLower(cmd[0]) == "authinfo" {
		s.handleAuth(args)
	} else {
		if isCommandAllowed(strings.ToLower(cmd[0])) {
			s.handleRequests()
		} else {
			t := &s.UserConnection
			log.Printf("Command %s not allowed", cmd[0])
			t.PrintfLine("502 %s not allowed", cmd[0])
			return
		}

	}
}

func (s *session) handleRequests() {

	if s.RequestsSinceUpdaate < 10 {
		s.RequestsSinceUpdaate += 1
	} else {
		s.updateSession()
		defer func() {
			s.RequestsSinceUpdaate = 0
		}()
	}

	if s.User.AllowanceUsed > s.User.Allowance {
		if time.Now().After(s.User.EndDate) {
			s.User.resetAllowance()
		} else {
			s.UserConnection.PrintfLine("502 Allowance Used")
			s.UserConnection.Close()
			return
		}
	}

	conn, err := connectionPool.Get()

	if err != nil {
		log.Printf("error 1 %v", err)
		//log.Printf("closing 2 %v", conn.Close())
		conn = nil
		connectionPool.Return(conn)
		s.handleRequests()
		return
	}

	// check nil here!
	err = conn.PrintfLine(s.command)

	if err != nil {
		log.Printf("error 2 %v", err)
		//log.Printf("closing 2 %v", conn.Close())
		conn = nil
		connectionPool.Return(conn)
		s.handleRequests()
		return
	}

	id, msg, err := conn.ReadCodeLine(220)
	if err != nil {
		if err == io.EOF {
			conn.Close()
			conn = nil
			connectionPool.Return(conn)
			s.handleRequests()
			return
		}
		//log.Printf("%v %v %v", err, id, msg)
		//s.UserConnection.PrintfLine("%s %s", id, msg)
		s.UserConnection.PrintfLine("%s %s", id, msg)
		connectionPool.Return(conn)
		return
	}
	parts := strings.SplitN(msg, " ", 2)
	if err != nil {
		log.Printf("%v", err)
		connectionPool.Return(conn)
		return
	}

	defer connectionPool.Return(conn)

	s.UserConnection.PrintfLine("220 1 %s", parts[1])
	writer := s.UserConnection.DotWriter()
	reader := conn.DotReader()
	bytes, _ := io.Copy(writer, reader)
	s.Bytes += bytes
	//log.Printf("%v", bytes)

	writer.Close()

}

func (s *session) handleAuthorization(user string, password string) bool {
	u := User{}
	err := db.Get(&u, "SELECT * FROM NewsUser WHERE username=?", user)
	log.Printf("%v", u)
	//log.Printf("%v", err)
	if err == nil && u.Password == password {
		s.User = u
		rows, err := db.NamedExec("INSERT INTO NewsSession (userid, bytes, conntime) VALUES (:id, 0, NOW())", map[string]interface{}{"id": u.Id})
		if err != nil {
			log.Printf("Failed inserting session")
			return false
		}
		id, err := rows.LastInsertId()
		if err != nil {
			log.Printf("Failed inserting session (getting last id)")
			return false
		}

		s.Session.Id = int(id)
		return true
	}
	return false

	/*for _, elem := range cfg.Users {
		if elem.Username == user && CheckPasswordHash(password, elem.Password) {
			return true
		}
	}
	return false*/
}

func (s *session) checkCache(user string, key string, value string) bool {
	val, err := cache.Get(user + ":" + key).Result()
	if err != nil {
		return false
	}

	if val == value {
		return true
	} else {
		return false
	}
}

func (s *session) getCache(user string, key string) string {
	val, _ := cache.Get(user + ":" + key).Result()
	return val
}

func (s *session) enterCache(user string, key string, val string, expire time.Duration) {
	err := cache.Set(user+":"+key, val, expire).Err()
	if err != nil {
		log.Printf("Cache error: %v", err)
	}
}

func (s *session) getUser() {
	u := User{}
	err := db.Get(&u, "SELECT * FROM NewsUser WHERE id=?", s.User.Id)
	if err == nil {
		s.User = u
	} else {
		log.Printf("failed update %v", err)
	}
}

func (s *session) handleAuth(args []string) {
	t := &s.UserConnection

	if len(args) < 2 {
		t.PrintfLine("502 Unknown Syntax!")
		s.UserConnection.Close()
		return
	}

	if strings.ToLower(args[0]) != "user" {
		t.PrintfLine("502 Unknown Syntax!")
		s.UserConnection.Close()
		return
	}

	t.PrintfLine("381 Continue")

	a, _ := t.ReadLine()
	parts := strings.SplitN(a, " ", 3)

	if strings.ToLower(parts[0]) != "authinfo" || strings.ToLower(parts[1]) != "pass" {
		t.PrintfLine("502 Unknown Syntax!")
		s.UserConnection.Close()
		return
	}

	if s.checkCache(args[1], "blocked", "true") {
		t.PrintfLine("502 Auth Failed")
		s.UserConnection.Close()
		return
	} else if s.checkCache(args[1], "allowance", "true") {
		t.PrintfLine("502 Allowance Used")
		s.UserConnection.Close()
		return
	} else if s.handleAuthorization(args[1], parts[2]) {
		if s.User.ConnUsed+1 > s.User.MaxConn {
			t.PrintfLine("502 Too many connections")
			s.UserConnection.Close()
		} else if s.User.AllowanceUsed >= s.User.Allowance {
			if s.User.Allowance != 0 && time.Now().After(s.User.EndDate) {
				s.User.resetAllowance()
			} else {
				t.PrintfLine("502 Allowance Used")
				s.enterCache(args[1], "allowance", "true", time.Minute)
				s.UserConnection.Close()
				return
			}
		}
		s.User.updateConnUsed(1)
		t.PrintfLine("281 Welcome")
	} else {
		t.PrintfLine("502 AUTH FAILED!")
		val := s.getCache(args[1], "authfailed")
		i := 0
		i, _ = strconv.Atoi(val)
		if (i + 1) > 100 {
			//log.Printf("cache blocked")
			s.enterCache(args[1], "blocked", "true", time.Minute)
			s.enterCache(args[1], "authfailed", strconv.Itoa(0), time.Minute)
		} else {
			s.enterCache(args[1], "authfailed", strconv.Itoa(i+1), time.Minute)
		}
		s.UserConnection.Close()
	}
}

func (u *User) resetAllowance() {
	_, err := db.Exec("UPDATE NewsUser SET allowanceused=0, enddate=DATE_ADD(enddate, INTERVAL period MONTH) WHERE username=?", u.UserName)
	if err != nil {
		log.Printf("Failed resetting user allowance")
		return
	}
	u.AllowanceUsed = 0
}

func (u *User) updateConnUsed(val int) {
	_, err := db.NamedExec("UPDATE NewsUser SET connused = connused + :val WHERE id=:id",
		map[string]interface{}{
			"val": val,
			"id":  u.Id,
		})
	if err != nil {
		log.Printf("Failed resetting user conn")
		return
	}
	u.ConnUsed += val
}

func (s *session) updateSession() {
	bytes := s.Bytes
	_, err := db.NamedExec("UPDATE NewsSession SET bytes = bytes + :bytes WHERE id=:id",
		map[string]interface{}{
			"bytes": bytes,
			"id":    s.Session.Id,
		})
	if err != nil {
		log.Printf("Failed updatating user session")
	}
	_, err = db.NamedExec("UPDATE NewsUser SET allowanceused = allowanceused + :bytes WHERE id=:id",
		map[string]interface{}{
			"bytes": bytes,
			"id":    s.User.Id,
		})
	if err != nil {
		log.Printf("Failed updatating user allowance")
	}
	//log.Printf("%v", bytes)
	s.getUser()
	s.Bytes = 0

}

func (s *session) closeSession() {
	s.User.updateConnUsed(-1)
	s.updateSession()
}

func connectBackend() (*textproto.Conn, error) {

	selectedBackend := &config.SelectedBackend{}

	// this was originally for handling multiple backends, for now we just use one
	for _, elem := range cfg.Backend {
		selectedBackend.BackendName = elem.BackendName
		selectedBackend.BackendAddr = elem.BackendAddr
		selectedBackend.BackendPort = elem.BackendPort
		selectedBackend.BackendTLS = elem.BackendTLS
		selectedBackend.BackendUser = elem.BackendUser
		selectedBackend.BackendPass = elem.BackendPass
		break
	}

	/*if len(selectedBackend.BackendAddr) == 0 && len(selectedBackend.BackendPort) == 0 {
		//t.PrintfLine("502 NO free backend connection!")
		return nil
	}*/

	var conn net.Conn
	var err error

	if selectedBackend.BackendTLS {

		conf := &tls.Config{
			InsecureSkipVerify: true,
		}

		conn, err = tls.Dial("tcp", selectedBackend.BackendAddr+":"+selectedBackend.BackendPort, conf)

		if err != nil {
			log.Printf("%v", err)
			log.Printf("%v:%v", selectedBackend.BackendAddr, selectedBackend.BackendPort)
			return nil, err
		}

	} else {
		// New backend connection to upstream NNTP
		conn, err = net.Dial("tcp", selectedBackend.BackendAddr+":"+selectedBackend.BackendPort)

		if err != nil {
			log.Printf("%v", err)
			log.Printf("%v:%v", selectedBackend.BackendAddr, selectedBackend.BackendPort)
			return nil, err
		}
	}

	c := textproto.NewConn(conn)

	_, _, err = c.ReadCodeLine(200)
	if err != nil {
		return nil, err
	}

	err = c.PrintfLine("authinfo user %s", selectedBackend.BackendUser)

	if err != nil {
		return nil, err
	}

	_, _, err = c.ReadCodeLine(381)
	if err != nil {
		return nil, err
	}

	err = c.PrintfLine("authinfo pass %s", selectedBackend.BackendPass)
	if err != nil {
		return nil, err
	}
	_, _, err = c.ReadCodeLine(281)

	if err == nil {

		log.Printf("[CONN] Connecting to Backend: %v", selectedBackend.BackendName)
		return c, nil

	} else {
		log.Printf("%v", err)
		backendConnections[selectedBackend.BackendName] -= 1
		//t.PrintfLine("502 Backend AUTH Failed!")
		return nil, err
	}
}

// Handles incoming requests.
func handleRequest(conn net.Conn) {

	c := textproto.NewConn(conn)

	sess := &session{
		*textproto.NewConn(conn),
		nil,
		"",
		nil,
		//p,
		User{},
		Session{},
		0,
		0,
	}

	c.PrintfLine("200 Welcome to NNTP Proxy!")

	for {
		l, err := c.ReadLine()
		if err != nil {

			// we'll keep all backend connections open, we'll reconnect on new requests
			/*if sess.selectedBackend != nil && len(sess.selectedBackend.BackendName) > 0 {
				backendConnections[sess.selectedBackend.BackendName] -= 1
				log.Printf("[CONN] Dropping Backend Connection: %v", sess.selectedBackend.BackendName)
			} else {
				log.Printf("[CONN] Error dropping Backend Connection cause selectedBackend is nil")
				log.Printf("%v", sess)
				sess.selectedBackend = nil

			}*/
			sess.closeSession()
			//log.Printf("%v", err)

			conn.Close()
			return

		}

		sess.command = l
		sess.dispatchCommand()
	}

}

var schema = `
select(1)
`

type User struct {
	Id            int       `db:"id"`
	UserName      string    `db:"username"`
	Password      string    `db:"password"`
	MaxConn       int       `db:"maxconn"`
	Allowance     int64     `db:"allowance"`
	EndDate       time.Time `db:"enddate"`
	AllowanceUsed int64     `db:"allowanceused"`
	Period        int       `db:"period"`
	ConnUsed      int       `db:"connused"`
}

type Session struct {
	Id       int       `db:"id"`
	UserId   int       `db:"userid"`
	Bytes    int64     `db:"bytes"`
	ConnTime time.Time `db:"conntime"`
}
