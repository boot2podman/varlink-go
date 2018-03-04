package varlink

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"syscall"
)

func keyList(mymap *map[string]Interface) []string {
	keys := make([]string, len(*mymap))

	i := 0
	for k := range *mymap {
		keys[i] = k
		i++
	}
	return keys
}

type Service struct {
	InterfaceDefinition
	vendor   string
	product  string
	version  string
	url      string
	services map[string]Interface
	quit     bool
}

func (s *Service) GetInfo(c Call) error {
	return c.Reply(&ServerOut{
		Parameters: GetInfo_Out{
			Vendor:     s.vendor,
			Product:    s.product,
			Version:    s.version,
			Url:        s.url,
			Interfaces: keyList(&s.services),
		},
	})
}

func (s *Service) GetInterfaceDescription(c Call) error {
	var in GetInterfaceDescription_In
	err := c.GetParameters(&in)
	if err != nil {
		return c.ReplyError("org.varlink.service.InvalidParameter", InvalidParameter_Error{Parameter: "interface"})
	}

	ifacep, ok := s.services[in.Interface]
	ifacen := ifacep.(Interface)
	if !ok {
		return c.ReplyError("org.varlink.service.InvalidParameter", InvalidParameter_Error{Parameter: "description"})
	}

	return c.Reply(&ServerOut{
		Parameters: GetInterfaceDescription_Out{ifacen.GetDescription()},
	})
}

func (s *Service) registerInterface(iface Interface) {
	name := iface.GetName()
	s.services[name] = iface
}

func (s *Service) HandleMessage(c serverCall, request []byte) error {
	var in ServerIn

	err := json.Unmarshal(request, &in)

	if err != nil {
		return err
	}
	c.in = &in
	r := strings.LastIndex(in.Method, ".")
	if r <= 0 {
		return c.ReplyError("org.varlink.service.InvalidParameter", InvalidParameter_Error{Parameter: "method"})
	}

	interfacename := in.Method[:r]
	methodname := in.Method[r+1:]
	_, ok := s.services[interfacename]

	if !ok {
		return c.ReplyError("org.varlink.service.InterfaceNotFound", InterfaceNotFound_Error{Interface: interfacename})
	}
	if !s.services[interfacename].IsMethod(methodname) {
		return c.ReplyError("org.varlink.service.MethodNotFound", MethodNotFound_Error{Method: methodname})
	}

	v := reflect.ValueOf(s.services[interfacename]).MethodByName(methodname)
	if v.Kind() != reflect.Func {
		return c.ReplyError("org.varlink.service.MethodNotImplemented", MethodNotImplemented_Error{Method: methodname})
	}

	args := []reflect.Value{
		reflect.ValueOf(&c),
	}
	ret := v.Call(args)

	if ret[0].Interface() == nil {
		return nil
	}

	return ret[0].Interface().(error)
}

func isActivated() bool {
	pid, err := strconv.Atoi(os.Getenv("LISTEN_PID"))
	if err != nil || pid != os.Getpid() {
		return false
	}

	nfds, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || nfds != 1 {
		return false
	}
	return true
}

func activationListener() net.Listener {
	defer os.Unsetenv("LISTEN_PID")
	defer os.Unsetenv("LISTEN_FDS")

	if !isActivated() {
		return nil
	}

	syscall.CloseOnExec(3)

	file := os.NewFile(uintptr(3), "LISTEN_FD_3")
	listener, err := net.FileListener(file)
	if err != nil {
		return nil
	}
	return listener
}

func (s *Service) Stop() {
	s.quit = true
}

func (s *Service) Run(address string) error {
	words := strings.SplitN(address, ":", 2)
	protocol := words[0]
	addr := words[1]

	// Ignore parameters after ';'
	words = strings.SplitN(addr, ";", 2)
	if words != nil {
		addr = words[0]
	}

	switch protocol {
	case "unix":
		if addr[0] != '@' {
			os.Remove(addr)
		}

	case "tcp":
		break

	default:
		return fmt.Errorf("Unknown protocol")
	}

	var l net.Listener
	l = activationListener()
	if l == nil {
		var err error
		l, err = net.Listen(protocol, addr)
		if err != nil {
			return err
		}
	}
	defer l.Close()
	s.quit = false

	handleConnection := func(conn net.Conn) {
		reader := bufio.NewReader(conn)
		c := serverCall{writer: bufio.NewWriter(conn)}

		for !s.quit {
			request, err := reader.ReadBytes('\x00')
			if err != nil {
				break
			}

			err = s.HandleMessage(c, request[:len(request)-1])
			if err != nil {
				break
			}
		}
		conn.Close()
		if s.quit {
			l.Close()
		}
	}

	for !s.quit {
		conn, err := l.Accept()
		if err != nil && !s.quit {
			return err
		}
		go handleConnection(conn)
	}

	return nil
}

func NewService(vendor string, product string, version string, url string, ifaces []Interface) Service {
	r := Service{
		InterfaceDefinition: NewInterfaceDefinition(),
		vendor:              vendor,
		product:             product,
		version:             version,
		url:                 url,
		services:            make(map[string]Interface),
	}

	// Register ourselves
	r.registerInterface(&r)

	for _, iface := range ifaces {
		r.registerInterface(iface)
	}
	return r
}