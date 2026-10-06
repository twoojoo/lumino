package ui

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"github.com/xo/dburl"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/knownhosts"
	"github.com/giovanni/lumino/internal/safety"
	"github.com/giovanni/lumino/internal/secrets"
)

var tlsModes = []string{"", "disable", "require", "verify-ca", "verify-full"}
var tlsLabels = []string{"Default", "Off", "Require", "Verify CA", "Verify full"}
var sshAuths = []string{"password", "key", "agent"}
var connColors = []string{"", "#e6b450", "#59c2ff", "#7fd962", "#d2a6ff", "#f29668", "#95e6cb", "#f07178"}

// ConnDialog creates or edits a connection.
type ConnDialog struct {
	app     *App
	editing string // connection id ("" = new)
	drivers []db.Driver
	drv     int
	done    bool
	d       dialog

	name, host, port, database, user, password, file, urlEd widget.Editor
	caFile, certFile, keyFile, serverName                   widget.Editor
	sshHost, sshPort, sshUser, sshKey, sshSecret            widget.Editor
	initSQL, params                                         widget.Editor
	savePw, useSSH, readOnly, createFile                    bool
	tlsMode, sshAuth, protected, color                      int // protected: 0 auto, 1 yes, 2 no
	drvBtns                                                 []widget.Clickable
	tlsBtns                                                 [5]widget.Clickable
	sshBtns                                                 [3]widget.Clickable
	protBtns                                                [3]widget.Clickable
	colorBtns                                               []widget.Clickable
	savePwBtn, sshBtn, roBtn, createBtn, showPw             widget.Clickable
	testBtn, saveBtn, connectBtn, cancelBtn, urlBtn, advBtn widget.Clickable
	advanced, pwVisible                                     bool
	pwKnown                                                 bool // a password was stored when the form opened
	testing                                                 bool
	testMsg                                                 string
	testOK                                                  bool
	err                                                     string
}

// openConnDialog edits a connection ("" = new).
func (a *App) openConnDialog(id string) {
	d := &ConnDialog{app: a, editing: id, drivers: db.Drivers(), savePw: true, colorBtns: make([]widget.Clickable, len(connColors))}
	d.drvBtns = make([]widget.Clickable, len(d.drivers))
	for _, ed := range []*widget.Editor{&d.name, &d.host, &d.port, &d.database, &d.user, &d.password, &d.file, &d.urlEd,
		&d.caFile, &d.certFile, &d.keyFile, &d.serverName, &d.sshHost, &d.sshPort, &d.sshUser, &d.sshKey, &d.sshSecret} {
		ed.SingleLine = true
	}
	d.password.Mask, d.sshSecret.Mask = '•', '•'
	if c, ok := a.prefs.Conn(id); ok {
		d.load(c)
	} else {
		d.editing = ""
		for i, dr := range d.drivers {
			if dr.Info().ID == "postgres" {
				d.drv = i
			}
		}
		d.host.SetText("localhost")
	}
	a.overlay = d
}

func (d *ConnDialog) info() db.Info { return d.drivers[d.drv].Info() }

func (d *ConnDialog) load(c *db.Config) {
	for i, dr := range d.drivers {
		if dr.Info().ID == c.Driver {
			d.drv = i
		}
	}
	d.name.SetText(c.Name)
	d.host.SetText(c.Host)
	if c.Port != 0 {
		d.port.SetText(strconv.Itoa(c.Port))
	}
	d.database.SetText(c.Database)
	d.user.SetText(c.User)
	d.file.SetText(c.File)
	if pw, ok := secrets.Get(c.ID, secrets.Password); ok {
		d.password.SetText(pw)
		d.pwKnown = true
	}
	d.savePw = c.SavePassword
	for i, m := range tlsModes {
		if m == c.TLS.Mode {
			d.tlsMode = i
		}
	}
	d.caFile.SetText(c.TLS.CAFile)
	d.certFile.SetText(c.TLS.CertFile)
	d.keyFile.SetText(c.TLS.KeyFile)
	d.serverName.SetText(c.TLS.ServerName)
	if c.SSH != nil && c.SSH.Host != "" {
		d.useSSH = true
		d.sshHost.SetText(c.SSH.Host)
		if c.SSH.Port != 0 {
			d.sshPort.SetText(strconv.Itoa(c.SSH.Port))
		}
		d.sshUser.SetText(c.SSH.User)
		d.sshKey.SetText(c.SSH.KeyFile)
		for i, a := range sshAuths {
			if a == c.SSH.Auth {
				d.sshAuth = i
			}
		}
		if s, ok := secrets.Get(c.ID, secrets.SSH); ok {
			d.sshSecret.SetText(s)
		}
	}
	d.initSQL.SetText(c.InitSQL)
	fileDrv := d.info().Has(db.FieldFile)
	var ps []string
	for k, v := range c.Params {
		if fileDrv && k == "create" {
			continue // shown as the "Create the file" toggle only
		}
		ps = append(ps, k+"="+v)
	}
	sort.Strings(ps)
	d.params.SetText(strings.Join(ps, "\n"))
	d.readOnly = c.ReadOnly
	switch {
	case c.Protected == nil:
		d.protected = 0
	case *c.Protected:
		d.protected = 1
	default:
		d.protected = 2
	}
	for i, col := range connColors {
		if col == c.Color {
			d.color = i
		}
	}
	d.createFile = c.Param("create") == "true"
	d.advanced = c.InitSQL != "" || len(ps) > 0
}

// config builds the connection from the form (secrets included).
func (d *ConnDialog) config() (db.Config, error) {
	info := d.info()
	// editing starts from the stored connection so that fields the form
	// doesn't show (Group, future ones) survive; every form field is reset
	// below and set only when the driver uses it.
	var c db.Config
	if d.editing != "" && d.app != nil && d.app.prefs != nil {
		if old, ok := d.app.prefs.Conn(d.editing); ok {
			c = *old
		}
	}
	c.ID, c.Driver, c.Name = d.editing, info.ID, strings.TrimSpace(d.name.Text())
	c.Host, c.Port, c.Database, c.User, c.File = "", 0, "", "", ""
	c.Password, c.SavePassword = "", false
	c.TLS, c.SSH, c.Params, c.Protected = db.TLSConfig{}, nil, nil, nil
	if c.ID == "" {
		c.ID = newID()
	}
	if info.Has(db.FieldHost) {
		c.Host = strings.TrimSpace(d.host.Text())
		if p := strings.TrimSpace(d.port.Text()); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n <= 0 || n > 65535 {
				return c, fmt.Errorf("invalid port %q", p)
			}
			c.Port = n
		}
	}
	if info.Has(db.FieldDatabase) {
		c.Database = strings.TrimSpace(d.database.Text())
	}
	if info.Has(db.FieldUser) {
		c.User = strings.TrimSpace(d.user.Text())
	}
	if info.Has(db.FieldPassword) {
		c.Password = d.password.Text()
		c.SavePassword = d.savePw
	}
	if info.Has(db.FieldFile) {
		c.File = strings.TrimSpace(d.file.Text())
		if c.File == "" {
			return c, fmt.Errorf("choose a database file")
		}
	}
	c.TLS = db.TLSConfig{Mode: tlsModes[d.tlsMode], CAFile: strings.TrimSpace(d.caFile.Text()),
		CertFile: strings.TrimSpace(d.certFile.Text()), KeyFile: strings.TrimSpace(d.keyFile.Text()),
		ServerName: strings.TrimSpace(d.serverName.Text())}
	if d.useSSH && info.Has(db.FieldHost) {
		s := &db.SSHConfig{Host: strings.TrimSpace(d.sshHost.Text()), User: strings.TrimSpace(d.sshUser.Text()),
			Auth: sshAuths[d.sshAuth], KeyFile: strings.TrimSpace(d.sshKey.Text()), Secret: d.sshSecret.Text()}
		if p := strings.TrimSpace(d.sshPort.Text()); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return c, fmt.Errorf("invalid SSH port %q", p)
			}
			s.Port = n
		}
		if s.Host == "" || s.User == "" {
			return c, fmt.Errorf("SSH tunnel: host and user are required")
		}
		if s.Auth == "key" && s.KeyFile == "" {
			return c, fmt.Errorf("SSH tunnel: choose the private key file")
		}
		c.SSH = s
	}
	c.InitSQL = strings.TrimSpace(d.initSQL.Text())
	for _, l := range strings.Split(d.params.Text(), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return c, fmt.Errorf("parameter %q: use key=value", l)
		}
		if c.Params == nil {
			c.Params = map[string]string{}
		}
		c.Params[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if info.Has(db.FieldFile) {
		// the toggle is the only source of "create" (load() keeps it out
		// of the Parameters text), so unchecking it really turns it off
		delete(c.Params, "create")
		if d.createFile {
			if c.Params == nil {
				c.Params = map[string]string{}
			}
			c.Params["create"] = "true"
		}
		if len(c.Params) == 0 {
			c.Params = nil
		}
	}
	c.ReadOnly = d.readOnly
	switch d.protected {
	case 1:
		on := true
		c.Protected = &on
	case 2:
		off := false
		c.Protected = &off
	}
	c.Color = connColors[d.color]
	if c.Name == "" {
		switch {
		case c.File != "":
			c.Name = shortPath(c.File)
		case c.Database != "":
			c.Name = c.Database + "@" + c.Host
		default:
			c.Name = info.Name + " " + c.Host
		}
	}
	return c, nil
}

// fromURL fills the form from a connection URL.
func (d *ConnDialog) fromURL(s string) error {
	u, err := dburl.Parse(strings.TrimSpace(s))
	if err != nil {
		return err
	}
	match := func(scheme string) int {
		for i, dr := range d.drivers {
			for _, sc := range dr.Info().Schemes {
				if strings.EqualFold(sc, scheme) {
					return i
				}
			}
		}
		return -1
	}
	i := -1
	for _, sc := range []string{u.OriginalScheme, u.SchemeName, u.Dialect, u.Driver} {
		if sc = strings.SplitN(sc, "+", 2)[0]; sc != "" && i < 0 {
			i = match(sc)
		}
	}
	if i < 0 {
		return fmt.Errorf("unsupported scheme %q", u.OriginalScheme)
	}
	d.drv = i
	info := d.info()
	if info.Has(db.FieldFile) {
		p := u.Opaque
		if p == "" {
			p = u.Path
		}
		d.file.SetText(p)
		return nil
	}
	d.host.SetText(u.Hostname())
	d.port.SetText(u.Port())
	if u.User != nil {
		d.user.SetText(u.User.Username())
		if pw, ok := u.User.Password(); ok {
			d.password.SetText(pw)
		}
	}
	d.database.SetText(strings.TrimPrefix(u.Path, "/"))
	q := u.Query()
	if m := q.Get("sslmode"); m != "" {
		for k, mode := range tlsModes {
			if mode == m {
				d.tlsMode = k
			}
		}
		q.Del("sslmode")
	}
	var ps []string
	for k := range q {
		ps = append(ps, k+"="+q.Get(k))
	}
	if len(ps) > 0 {
		d.params.SetText(strings.Join(ps, "\n"))
		d.advanced = true
	}
	return nil
}

func (d *ConnDialog) Done() bool { return d.done }

// save stores the connection and its secrets.
func (d *ConnDialog) save(connect bool) {
	a := d.app
	c, err := d.config()
	if err != nil {
		d.err = err.Error()
		return
	}
	if err := d.storeSecrets(&c); err != nil {
		d.err = "Keyring: " + err.Error()
		return
	}
	old, existed := a.prefs.Conn(c.ID)
	changed := existed && connChanged(old, &c)
	a.prefs.PutConn(c)
	a.savePrefs()
	d.done = true
	open := func() {
		if !connect {
			return
		}
		a.sidebar.expand(connKey(c.ID), true)
		a.connect(c.ID, func(cs *connState) {
			if a.findTab(func(t Tab) bool { return t.ConnID() == c.ID }) < 0 {
				a.newConsole(c.ID, "", "")
			}
		})
	}
	a.flash("Saved "+c.Name, false)
	if changed {
		// reconnect with the new settings (asking first when work is open)
		a.requestDisconnect(c.ID, open)
		return
	}
	open()
}

// storeSecrets writes the password / SSH secret of the form.
//
// An empty password field is ambiguous: in a new session a "don't save"
// password is not known, so the field opens empty. Storing "" then would
// make connect use an empty password without asking. So:
//   - saved password: stored (even empty: a valid password) unless the
//     field is empty and no password was known when the form opened
//     (keyring cleared/unavailable): connect will ask;
//   - not saved: a typed password is kept for the session; an empty field
//     forgets it, so connect asks. Either way the keyring copy is removed.
func (d *ConnDialog) storeSecrets(c *db.Config) error {
	if d.info().Has(db.FieldPassword) {
		switch {
		case c.SavePassword && (c.Password != "" || d.pwKnown || d.editing == ""):
			if err := secrets.Set(c.ID, secrets.Password, c.Password, true); err != nil {
				return err
			}
		case c.SavePassword:
			// unknown and empty: leave it unset, connect asks
		case c.Password != "":
			if err := secrets.Set(c.ID, secrets.Password, c.Password, false); err != nil {
				return err
			}
		default:
			secrets.Delete(c.ID, secrets.Password)
		}
	}
	persistSSH := c.SavePassword || !d.info().Has(db.FieldPassword)
	switch {
	case c.SSH != nil && c.SSH.Secret != "":
		_ = secrets.Set(c.ID, secrets.SSH, c.SSH.Secret, persistSSH)
	case !persistSSH:
		secrets.Delete(c.ID, secrets.SSH) // "don't save" removes the keyring copy
	}
	return nil
}

func connChanged(a, b *db.Config) bool {
	return a.Driver != b.Driver || a.Host != b.Host || a.Port != b.Port || a.Database != b.Database || a.User != b.User ||
		a.File != b.File || a.TLS != b.TLS || fmt.Sprint(a.SSH) != fmt.Sprint(b.SSH) || fmt.Sprint(a.Params) != fmt.Sprint(b.Params) ||
		a.InitSQL != b.InitSQL || a.ReadOnly != b.ReadOnly
}

// test opens and closes a connection with the form settings.
func (d *ConnDialog) test() {
	a := d.app
	c, err := d.config()
	if err != nil {
		d.testMsg, d.testOK = err.Error(), false
		return
	}
	if err := safety.CheckConnection(&c); err != nil {
		d.testMsg, d.testOK = err.Error(), false
		return
	}
	d.testing, d.testMsg = true, "Connecting…"
	hostKey := knownhosts.Callback(func(host, fp string) bool {
		// the test trusts nothing new silently: report the fingerprint
		a.post(func() { d.testMsg = "SSH host " + host + " is unknown (" + fp + "): connect once to trust it" })
		return false
	})
	go func() {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), db.ConnectTimeout)
		defer cancel()
		conn, err := db.Open(ctx, &c, db.OpenOptions{HostKey: hostKey})
		msg := ""
		if err == nil {
			err = conn.Ping(ctx)
			msg = fmt.Sprintf("Connected in %s — %s", fmtDur(time.Since(start)), conn.ServerVersion())
			conn.Close()
		}
		a.post(func() {
			d.testing = false
			if err != nil {
				if strings.HasPrefix(d.testMsg, "SSH host") {
					return
				}
				d.testMsg, d.testOK = err.Error(), false
				return
			}
			d.testMsg, d.testOK = msg, true
		})
	}()
}

func (d *ConnDialog) Layout(gtx C) D {
	a, th := d.app, d.app.th
	for i := range d.drvBtns {
		if d.drvBtns[i].Clicked(gtx) {
			d.drv = i
			d.testMsg = ""
		}
	}
	for i := range d.tlsBtns {
		if d.tlsBtns[i].Clicked(gtx) {
			d.tlsMode = i
		}
	}
	for i := range d.sshBtns {
		if d.sshBtns[i].Clicked(gtx) {
			d.sshAuth = i
		}
	}
	for i := range d.protBtns {
		if d.protBtns[i].Clicked(gtx) {
			d.protected = i
		}
	}
	for i := range d.colorBtns {
		if d.colorBtns[i].Clicked(gtx) {
			d.color = i
		}
	}
	if d.savePwBtn.Clicked(gtx) {
		d.savePw = !d.savePw
	}
	if d.sshBtn.Clicked(gtx) {
		d.useSSH = !d.useSSH
	}
	if d.roBtn.Clicked(gtx) {
		d.readOnly = !d.readOnly
	}
	if d.createBtn.Clicked(gtx) {
		d.createFile = !d.createFile
	}
	if d.advBtn.Clicked(gtx) {
		d.advanced = !d.advanced
	}
	if d.showPw.Clicked(gtx) {
		d.pwVisible = !d.pwVisible
		if d.pwVisible {
			d.password.Mask = 0
		} else {
			d.password.Mask = '•'
		}
	}
	if d.urlBtn.Clicked(gtx) {
		if err := d.fromURL(d.urlEd.Text()); err != nil {
			d.err = "URL: " + err.Error()
		} else {
			d.err = ""
			d.urlEd.SetText("")
		}
	}
	if d.testBtn.Clicked(gtx) && !d.testing {
		d.test()
	}
	if d.saveBtn.Clicked(gtx) {
		d.save(false)
	}
	if d.connectBtn.Clicked(gtx) {
		d.save(true)
	}
	if d.cancelBtn.Clicked(gtx) || d.d.close.Clicked(gtx) {
		d.done = true
	}
	info := d.info()
	title := "New connection"
	if d.editing != "" {
		title = "Edit connection"
	}
	lw := unit.Dp(120)
	field := func(label string, ed *widget.Editor, hint string) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return formRow(gtx, th, label, lw, func(gtx C) D { return textField(gtx, th, ed, hint, false) })
		})
	}
	return d.d.layout(gtx, th, 720, icDatabase, title, func(gtx C) D {
		var ch []layout.FlexChild
		ch = append(ch,
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Database") }),
			layout.Rigid(func(gtx C) D { return d.layoutDrivers(gtx, th) }),
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Connection") }),
			field("Name", &d.name, "my database"),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "From URL", lw, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D {
							return textField(gtx, th, &d.urlEd, "paste postgres://user:pass@host:5432/db …", true)
						}),
						layout.Rigid(layout.Spacer{Width: 6}.Layout),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &d.urlBtn, nil, "Fill", pal.Text, pal.Hover) }),
					)
				})
			}),
		)
		if info.Has(db.FieldFile) {
			ch = append(ch, field("File", &d.file, "/path/to/database.sqlite"),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: lw}.Layout(gtx, func(gtx C) D {
						return toggle(gtx, th, &d.createBtn, "Create the file if it doesn't exist", d.createFile)
					})
				}))
		}
		if info.Has(db.FieldHost) {
			ch = append(ch, layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Host", lw, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D { return textField(gtx, th, &d.host, "localhost", false) }),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.Text, "Port") }),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(90), gtx.Dp(90)
							return textField(gtx, th, &d.port, strconv.Itoa(info.DefaultPort), true)
						}),
					)
				})
			}))
		}
		if info.Has(db.FieldDatabase) {
			lbl := info.DatabaseLabel
			if lbl == "" {
				lbl = "Database"
			}
			ch = append(ch, field(lbl, &d.database, info.DefaultDB))
		}
		if info.Has(db.FieldUser) {
			ch = append(ch, field("User", &d.user, info.DefaultUser))
		}
		if info.Has(db.FieldPassword) {
			ch = append(ch, layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Password", lw, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D { return textField(gtx, th, &d.password, "", false) }),
						layout.Rigid(func(gtx C) D {
							ic := icEye
							if d.pwVisible {
								ic = icEyeOff
							}
							return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D { return iconOnly(gtx, &d.showPw, ic, 16) })
						}),
					)
				})
			}), layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: lw}.Layout(gtx, func(gtx C) D {
					label := "Save the password in the system keyring"
					if !secrets.Available() {
						label = "Remember the password for this session (no system keyring available)"
					}
					return toggle(gtx, th, &d.savePwBtn, label, d.savePw)
				})
			}))
		}
		if info.Caps.TLS {
			ch = append(ch,
				layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "TLS / SSL") }),
				layout.Rigid(func(gtx C) D {
					return formRow(gtx, th, "Mode", lw, func(gtx C) D { return segmented(gtx, th, d.tlsBtns[:], tlsLabels, d.tlsMode) })
				}),
			)
			if d.tlsMode >= 2 {
				ch = append(ch, field("CA certificate", &d.caFile, "/path/ca.pem (empty = system roots)"),
					field("Client cert", &d.certFile, "optional"), field("Client key", &d.keyFile, "optional"))
				if d.tlsMode == 4 {
					ch = append(ch, field("Server name", &d.serverName, "host name in the certificate (default: host)"))
				}
			}
		}
		if info.Has(db.FieldHost) {
			ch = append(ch,
				layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "SSH tunnel") }),
				layout.Rigid(func(gtx C) D { return toggle(gtx, th, &d.sshBtn, "Connect through an SSH bastion", d.useSSH) }),
			)
			if d.useSSH {
				ch = append(ch,
					layout.Rigid(func(gtx C) D {
						return formRow(gtx, th, "SSH host", lw, func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Flexed(1, func(gtx C) D { return textField(gtx, th, &d.sshHost, "bastion.example.com", false) }),
								layout.Rigid(layout.Spacer{Width: 8}.Layout),
								layout.Rigid(func(gtx C) D {
									gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(90), gtx.Dp(90)
									return textField(gtx, th, &d.sshPort, "22", true)
								}),
							)
						})
					}),
					field("SSH user", &d.sshUser, ""),
					layout.Rigid(func(gtx C) D {
						return formRow(gtx, th, "Auth", lw, func(gtx C) D {
							return segmented(gtx, th, d.sshBtns[:], []string{"Password", "Private key", "SSH agent"}, d.sshAuth)
						})
					}),
				)
				switch d.sshAuth {
				case 0:
					ch = append(ch, field("SSH password", &d.sshSecret, ""))
				case 1:
					ch = append(ch, field("Key file", &d.sshKey, "~/.ssh/id_ed25519"), field("Passphrase", &d.sshSecret, "if the key is encrypted"))
				}
			}
		}
		ch = append(ch,
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Safety") }),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Production", lw, func(gtx C) D {
					return segmented(gtx, th, d.protBtns[:], []string{"Auto (by name)", "Protected", "Not protected"}, d.protected)
				})
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: lw}.Layout(gtx, func(gtx C) D {
					l := material.Body2(th, "Protected: red frame, manual transactions by default, confirmations for changes (typed for destructive ones).")
					l.Color, l.TextSize = pal.TextDim, 11.5
					return l.Layout(gtx)
				})
			}),
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: lw}.Layout(gtx, func(gtx C) D {
					return toggle(gtx, th, &d.roBtn, "Read-only: block every statement that changes data or schema", d.readOnly)
				})
			}),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Color", lw, func(gtx C) D { return d.layoutColors(gtx) })
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
					ic := icCollapse
					if d.advanced {
						ic = icExpand
					}
					return d.advBtn.Layout(gtx, func(gtx C) D { return iconText(gtx, th, ic, 12, pal.TextDim, "Advanced") })
				})
			}),
		)
		if d.advanced {
			ch = append(ch,
				layout.Rigid(layout.Spacer{Height: 8}.Layout),
				layout.Rigid(func(gtx C) D {
					return formRow(gtx, th, "Parameters", lw, func(gtx C) D { return textArea(gtx, th, &d.params, "driver options, one key=value per line", 70) })
				}),
				layout.Rigid(func(gtx C) D {
					return formRow(gtx, th, "Init SQL", lw, func(gtx C) D {
						return textArea(gtx, th, &d.initSQL, "run on every new session, e.g. SET search_path TO app", 70)
					})
				}),
			)
		}
		ch = append(ch, layout.Rigid(func(gtx C) D {
			if d.err == "" {
				return D{}
			}
			return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D { return iconText(gtx, th, icError, 12, pal.Err, d.err) })
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
	}, footerButtons(func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &d.testBtn, icPlug, "Test", pal.Text, pal.Hover) }),
			layout.Rigid(layout.Spacer{Width: 10}.Layout),
			layout.Flexed(1, func(gtx C) D {
				if d.testMsg == "" {
					return D{}
				}
				c := pal.Err
				if d.testOK {
					c = pal.Ok
				} else if d.testing {
					c = pal.TextDim
				}
				return a.tipLbl(gtx, "conntest", fontSans, 12, c, d.testMsg)
			}),
		)
	},
		func(gtx C) D { return pillButton(gtx, th, &d.cancelBtn, nil, "Cancel", pal.Text, pal.Hover) },
		func(gtx C) D { return pillButton(gtx, th, &d.saveBtn, nil, "Save", pal.Text, pal.Hover) },
		func(gtx C) D { return button(gtx, th, &d.connectBtn, "Save & connect", pal.Accent) },
	))
}

// layoutDrivers draws the driver tiles.
func (d *ConnDialog) layoutDrivers(gtx C, th *material.Theme) D {
	perRow := 5
	var rows []layout.FlexChild
	for start := 0; start < len(d.drivers); start += perRow {
		start := start
		rows = append(rows, layout.Rigid(func(gtx C) D {
			var ch []layout.FlexChild
			for i := start; i < min(start+perRow, len(d.drivers)); i++ {
				i := i
				ch = append(ch, layout.Flexed(1, func(gtx C) D {
					return layout.Inset{Right: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
						return d.drvBtns[i].Layout(gtx, func(gtx C) D {
							sel := i == d.drv
							b, c := pal.Bg, pal.Text
							if sel {
								b, c = pal.Selected, pal.TextBright
							} else if d.drvBtns[i].Hovered() {
								b = pal.Hover
							}
							border := pal.Border
							if sel {
								border = pal.Accent
							}
							return rborder(gtx, border, b, 6, func(gtx C) D {
								gtx.Constraints.Min.X = gtx.Constraints.Max.X
								return insetHV(8, 7).Layout(gtx, func(gtx C) D {
									return iconText(gtx, th, icDatabase, 12, c, d.drivers[i].Info().Name)
								})
							})
						})
					})
				}))
			}
			for i := len(ch); i < perRow; i++ {
				ch = append(ch, layout.Flexed(1, func(gtx C) D { return D{} }))
			}
			return layout.Flex{}.Layout(gtx, ch...)
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

func (d *ConnDialog) layoutColors(gtx C) D {
	var ch []layout.FlexChild
	for i, c := range connColors {
		i, c := i, c
		ch = append(ch, layout.Rigid(func(gtx C) D {
			return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D {
				return d.colorBtns[i].Layout(gtx, func(gtx C) D {
					col, ok := parseHex(c)
					if !ok {
						col = pal.Accent
					}
					border := pal.Border
					if i == d.color {
						border = pal.TextBright
					}
					return rborder(gtx, border, pal.Bg, 12, func(gtx C) D {
						return inset(3).Layout(gtx, func(gtx C) D {
							if c == "" {
								return dotLabel(gtx, d.app.th, "auto")
							}
							return dot(gtx, col, 16)
						})
					})
				})
			})
		}))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
}

func dotLabel(gtx C, th *material.Theme, s string) D {
	return insetHV(4, 1).Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 11, pal.TextDim, s) })
}

var _ = url.Values{}
