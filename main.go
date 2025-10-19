package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	serverName      = "GoDLNA-DMR/1.0"
	deviceUUID      = "uuid:12345678-90ab-cdef-1234-567890abcdef" // TODO: 生成并持久化
	httpPort        = 8200
	ssdpAddr        = "239.255.255.250:1900"
	deviceType      = "urn:schemas-upnp-org:device:MediaRenderer:1"
	avTransportType = "urn:schemas-upnp-org:service:AVTransport:1"
	renderingType   = "urn:schemas-upnp-org:service:RenderingControl:1"
)

type PlayerState struct {
	mu             sync.RWMutex
	TransportURI   string
	TransportMeta  string
	TransportState string // STOPPED | PLAYING | PAUSED_PLAYBACK | TRANSITIONING
	Volume         int    // 0..100
	Mute           bool
}

var state = &PlayerState{
	TransportState: "STOPPED",
	Volume:         50,
	Mute:           false,
}

func main() {
	ip, err := firstUsableIPv4()
	if err != nil {
		log.Fatalf("no IPv4: %v", err)
	}
	baseURL := fmt.Sprintf("http://%s:%d", ip, httpPort)

	// HTTP server (device desc, scpd, control, event)
	mux := http.NewServeMux()
	registerHTTP(mux, baseURL)
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", httpPort),
		Handler: logMiddleware(mux),
	}

	// SSDP
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ssdpAnnounce(ctx, baseURL)
	go ssdpSearchResponder(ctx, baseURL)

	// start HTTP
	go func() {
		log.Printf("HTTP listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP error: %v", err)
		}
	}()

	// graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	ctxShutdown, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	_ = srv.Shutdown(ctxShutdown)
	log.Println("bye")
}

func firstUsableIPv4() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&(net.FlagUp|net.FlagLoopback) != net.FlagUp {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				ip := ipn.IP.To4()
				if !ip.IsLoopback() {
					return ip.String(), nil
				}
			}
		}
	}
	return "", fmt.Errorf("no IPv4 found")
}

func registerHTTP(mux *http.ServeMux, base string) {
	mux.HandleFunc("/device.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		io.WriteString(w, deviceDescriptionXML(base))
	})
	mux.HandleFunc("/upnp/service/avtransport.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		io.WriteString(w, scpdAVTransportXML())
	})
	mux.HandleFunc("/upnp/service/renderingcontrol.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		io.WriteString(w, scpdRenderingXML())
	})
	mux.HandleFunc("/upnp/control/avtransport", avTransportControl)
	mux.HandleFunc("/upnp/control/renderingcontrol", renderingControl)
	// Event callback endpoints (notifying subscribers). For simplicity, we won’t implement GENA yet.
	mux.HandleFunc("/upnp/event/avtransport", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/upnp/event/renderingcontrol", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Root
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "Go DLNA DMR running\n")
	})
}

func deviceDescriptionXML(base string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <deviceType>%s</deviceType>
    <friendlyName>Go IINA Renderer</friendlyName>
    <manufacturer>GoDLNA</manufacturer>
    <modelName>GoDLNA-DMR</modelName>
    <UDN>%s</UDN>
    <serviceList>
      <service>
        <serviceType>%s</serviceType>
        <serviceId>urn:upnp-org:serviceId:AVTransport</serviceId>
        <SCPDURL>/upnp/service/avtransport.xml</SCPDURL>
        <controlURL>/upnp/control/avtransport</controlURL>
        <eventSubURL>/upnp/event/avtransport</eventSubURL>
      </service>
      <service>
        <serviceType>%s</serviceType>
        <serviceId>urn:upnp-org:serviceId:RenderingControl</serviceId>
        <SCPDURL>/upnp/service/renderingcontrol.xml</SCPDURL>
        <controlURL>/upnp/control/renderingcontrol</controlURL>
        <eventSubURL>/upnp/event/renderingcontrol</eventSubURL>
      </service>
    </serviceList>
    <presentationURL>%s/</presentationURL>
  </device>
</root>`, deviceType, deviceUUID, avTransportType, renderingType, base)
}

func scpdAVTransportXML() string {
	// Minimal actions: SetAVTransportURI, Play, Pause, Stop
	return `<?xml version="1.0" encoding="utf-8"?>
<scpd xmlns="urn:schemas-upnp-org:service-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <actionList>
    <action>
      <name>SetAVTransportURI</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>CurrentURI</name><direction>in</direction><relatedStateVariable>AVTransportURI</relatedStateVariable></argument>
        <argument><name>CurrentURIMetaData</name><direction>in</direction><relatedStateVariable>AVTransportURIMetaData</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action>
      <name>Play</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>Speed</name><direction>in</direction><relatedStateVariable>TransportPlaySpeed</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action>
      <name>Pause</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action>
      <name>Stop</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
      </argumentList>
    </action>
  </actionList>
  <serviceStateTable>
    <stateVariable sendEvents="yes"><name>TransportState</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>AVTransportURI</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>AVTransportURIMetaData</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>TransportPlaySpeed</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_InstanceID</name><dataType>ui4</dataType></stateVariable>
  </serviceStateTable>
</scpd>`
}

func scpdRenderingXML() string {
	return `<?xml version="1.0" encoding="utf-8"?>
<scpd xmlns="urn:schemas-upnp-org:service-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <actionList>
    <action>
      <name>SetVolume</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>Channel</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Channel</relatedStateVariable></argument>
        <argument><name>DesiredVolume</name><direction>in</direction><relatedStateVariable>Volume</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action>
      <name>GetVolume</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>Channel</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Channel</relatedStateVariable></argument>
        <argument><name>CurrentVolume</name><direction>out</direction><relatedStateVariable>Volume</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action>
      <name>SetMute</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>Channel</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Channel</relatedStateVariable></argument>
        <argument><name>DesiredMute</name><direction>in</direction><relatedStateVariable>Mute</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action>
      <name>GetMute</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>Channel</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Channel</relatedStateVariable></argument>
        <argument><name>CurrentMute</name><direction>out</direction><relatedStateVariable>Mute</relatedStateVariable></argument>
      </argumentList>
    </action>
  </actionList>
  <serviceStateTable>
    <stateVariable sendEvents="no"><name>Volume</name><dataType>ui2</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>Mute</name><dataType>boolean</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_InstanceID</name><dataType>ui4</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_Channel</name><dataType>string</dataType></stateVariable>
  </serviceStateTable>
</scpd>`
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// --------------- SSDP ---------------

func ssdpAnnounce(ctx context.Context, base string) {
	addr, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("ssdp announce dial err: %v", err)
		return
	}
	defer conn.Close()

	usns := []struct{ st, usn string }{
		{deviceType, deviceUUID + "::" + deviceType},
		{avTransportType, deviceUUID + "::" + avTransportType},
		{renderingType, deviceUUID + "::" + renderingType},
		{"upnp:rootdevice", deviceUUID + "::upnp:rootdevice"},
		{deviceUUID, deviceUUID},
	}

	ticker := time.NewTicker(30 * time.Second) // DLNA 推荐 30~1800s
	defer ticker.Stop()

	for {
		for _, x := range usns {
			msg := fmt.Sprintf(
				"NOTIFY * HTTP/1.1\r\nHOST: %s\r\nCACHE-CONTROL: max-age=1800\r\nLOCATION: %s/device.xml\r\nNT: %s\r\nNTS: ssdp:alive\r\nSERVER: %s\r\nUSN: %s\r\nBOOTID.UPNP.ORG: 1\r\nCONFIGID.UPNP.ORG: 1\r\n\r\n",
				ssdpAddr, base, x.st, serverName, x.usn)
			_, _ = conn.Write([]byte(msg))
		}
		select {
		case <-ctx.Done():
			// send byebye
			for _, x := range usns {
				msg := fmt.Sprintf(
					"NOTIFY * HTTP/1.1\r\nHOST: %s\r\nNT: %s\r\nNTS: ssdp:byebye\r\nUSN: %s\r\n\r\n",
					ssdpAddr, x.st, x.usn)
				_, _ = conn.Write([]byte(msg))
			}
			return
		case <-ticker.C:
		}
	}
}

func ssdpSearchResponder(ctx context.Context, base string) {
	addr, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
	l, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("ssdp listen err: %v", err)
		return
	}
	defer l.Close()
	_ = l.SetReadBuffer(65536)
	buf := make([]byte, 8192)
	for {
		l.SetDeadline(time.Now().Add(2 * time.Second))
		n, src, err := l.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			log.Printf("ssdp read err: %v", err)
			continue
		}
		text := string(buf[:n])
		if !strings.HasPrefix(text, "M-SEARCH * HTTP/1.1") {
			continue
		}
		if !strings.Contains(strings.ToUpper(text), "MAN: \"SSDP:DISCOVER\"") {
			continue
		}
		st := headerValue(text, "ST")
		if st == "" {
			continue
		}
		// We respond to rootdevice, deviceType, service types, and UUID
		valid := st == "ssdp:all" || st == "upnp:rootdevice" || st == deviceType || st == avTransportType || st == renderingType || st == deviceUUID
		if !valid {
			continue
		}
		usn := deviceUUID
		if st != deviceUUID && st != "ssdp:all" {
			usn = deviceUUID + "::" + st
		}
		maxAge := 1800
		resp := fmt.Sprintf(
			"HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=%d\r\nDATE: %s\r\nEXT:\r\nLOCATION: %s/device.xml\r\nSERVER: %s\r\nST: %s\r\nUSN: %s\r\nBOOTID.UPNP.ORG: 1\r\nCONFIGID.UPNP.ORG: 1\r\n\r\n",
			maxAge, time.Now().UTC().Format(time.RFC1123), base, serverName, st, usn)
		_, _ = l.WriteToUDP([]byte(resp), src)
	}
}

func headerValue(raw, key string) string {
	lines := strings.Split(raw, "\r\n")
	key = strings.ToUpper(key)
	for _, ln := range lines {
		if i := strings.IndexByte(ln, ':'); i > 0 {
			k := strings.ToUpper(strings.TrimSpace(ln[:i]))
			if k == key {
				return strings.TrimSpace(ln[i+1:])
			}
		}
	}
	return ""
}

// --------------- SOAP Control ---------------

func avTransportControl(w http.ResponseWriter, r *http.Request) {
	// Parse SOAPAction
	sa := r.Header.Get("SOAPACTION")
	sa = strings.Trim(sa, "\"")
	action := ""
	if i := strings.LastIndex(sa, "#"); i >= 0 {
		action = sa[i+1:]
	}
	body, _ := io.ReadAll(r.Body)
	switch action {
	case "SetAVTransportURI":
		curURI := xmlText(body, "CurrentURI")
		curMeta := xmlText(body, "CurrentURIMetaData")
		state.mu.Lock()
		state.TransportURI = curURI
		state.TransportMeta = curMeta
		state.TransportState = "STOPPED"
		state.mu.Unlock()
		writeSOAPOK(w, "SetAVTransportURIResponse")
	case "Play":
		// Launch IINA with current URI
		state.mu.RLock()
		uri := state.TransportURI
		state.mu.RUnlock()
		if uri == "" {
			writeSOAPError(w, 714, "No content selected")
			return
		}
		if err := playWithIINA(uri); err != nil {
			log.Printf("iina error: %v", err)
			writeSOAPError(w, 701, "Playback failed")
			return
		}
		state.mu.Lock()
		state.TransportState = "PLAYING"
		state.mu.Unlock()
		writeSOAPOK(w, "PlayResponse")
	case "Pause":
		pauseIINA()
		state.mu.Lock()
		state.TransportState = "PAUSED_PLAYBACK"
		state.mu.Unlock()
		writeSOAPOK(w, "PauseResponse")
	case "Stop":
		stopIINA()
		state.mu.Lock()
		state.TransportState = "STOPPED"
		state.mu.Unlock()
		writeSOAPOK(w, "StopResponse")
	default:
		writeSOAPError(w, 401, "Invalid Action")
	}
}

func renderingControl(w http.ResponseWriter, r *http.Request) {
	sa := r.Header.Get("SOAPACTION")
	sa = strings.Trim(sa, "\"")
	action := ""
	if i := strings.LastIndex(sa, "#"); i >= 0 {
		action = sa[i+1:]
	}
	body, _ := io.ReadAll(r.Body)
	switch action {
	case "SetVolume":
		volStr := xmlText(body, "DesiredVolume")
		v, _ := strconv.Atoi(volStr)
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		state.mu.Lock()
		state.Volume = v
		state.mu.Unlock()
		// Optional: send AppleScript to set system or IINA volume mapping
		writeSOAPOK(w, "SetVolumeResponse")
	case "GetVolume":
		state.mu.RLock()
		v := state.Volume
		state.mu.RUnlock()
		writeSOAPOKWithBody(w, "GetVolumeResponse", fmt.Sprintf("<CurrentVolume>%d</CurrentVolume>", v))
	case "SetMute":
		muteStr := strings.ToLower(xmlText(body, "DesiredMute"))
		m := muteStr == "1" || muteStr == "true"
		state.mu.Lock()
		state.Mute = m
		state.mu.Unlock()
		writeSOAPOK(w, "SetMuteResponse")
	case "GetMute":
		state.mu.RLock()
		m := state.Mute
		state.mu.RUnlock()
		v := "0"
		if m {
			v = "1"
		}
		writeSOAPOKWithBody(w, "GetMuteResponse", fmt.Sprintf("<CurrentMute>%s</CurrentMute>", v))
	default:
		writeSOAPError(w, 401, "Invalid Action")
	}
}

func xmlText(b []byte, tag string) string {
	// naive extraction for demo; consider real SOAP/XML parsing
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	s := string(b)
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	i += len(open)
	j := strings.Index(s[i:], close)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[i : i+j])
}

func writeSOAPOK(w http.ResponseWriter, respName string) {
	writeSOAPOKWithBody(w, respName, "")
}

func writeSOAPOKWithBody(w http.ResponseWriter, respName, inner string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	env := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:%s xmlns:u="%s">%s</u:%s>
  </s:Body>
</s:Envelope>`, respName, avTransportType, inner, respName)
	io.WriteString(w, env)
}

func writeSOAPError(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(500)
	env := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <s:Fault>
      <faultcode>s:Client</faultcode>
      <faultstring>UPnPError</faultstring>
      <detail>
        <UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
          <errorCode>%d</errorCode>
          <errorDescription>%s</errorDescription>
        </UPnPError>
      </detail>
    </s:Fault>
  </s:Body>
</s:Envelope>`, code, desc)
	io.WriteString(w, env)
}

// --------------- IINA integration ---------------

func playWithIINA(uri string) error {
	// Prefer iina-cli if installed
	if _, err := os.Stat("/usr/local/bin/iina-cli"); err == nil {
		return exec.Command("/usr/local/bin/iina-cli", uri).Start()
	}
	if _, err := os.Stat("/opt/homebrew/bin/iina-cli"); err == nil {
		return exec.Command("/opt/homebrew/bin/iina-cli", uri).Start()
	}
	// Fallback to app binary
	app := "/Applications/IINA.app/Contents/MacOS/iina"
	if _, err := os.Stat(app); err == nil {
		return exec.Command(app, "--no-stdin", uri).Start()
	}
	// AppleScript fallback
	script := fmt.Sprintf(`tell application "IINA"
        activate
        open location "%s"
    end tell`, escapeAppleScript(uri))
	return exec.Command("osascript", "-e", script).Start()
}

func pauseIINA() {
	// AppleScript to toggle pause
	script := `tell application "IINA" to pause`
	_ = exec.Command("osascript", "-e", script).Run()
}

func stopIINA() {
	// Stop current playback
	script := `tell application "IINA" to stop`
	_ = exec.Command("osascript", "-e", script).Run()
}

func escapeAppleScript(s string) string {
	r := strings.ReplaceAll(s, `\`, `\\`)
	r = strings.ReplaceAll(r, `"`, `\"`)
	return r
}
