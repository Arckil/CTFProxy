package main

import (
	"errors"
	"go.starlark.net/starlark"
	"log"
	"net/http"
	"time"
)

func hasAccess(servicename, username string, groups []string, req *http.Request) *UPError {
	for _, g := range groups {
		if g == "break-glass-access@groups."+_configuration.CorpDomain {
			return nil
		}
	}
	code := _configuration.AccessPolicies.ConfigOrNil().(map[string]string)[servicename]
	if code == "" {
		return NewUPError(http.StatusBadRequest, "Could not resolve the IP address for host "+req.Host, "Your client has issued a malformed or illegal request.", "", "_configuration.AccessPolicies["+servicename+"] not found")
	}
	// log.Println(servicename)
	// log.Println(code)
	thread := &starlark.Thread{
		Name: "access",
		Print: func(_ *starlark.Thread, msg string) {
			log.Printf("access starlark print: " + msg)
		},
	}
	ret := make(chan *UPError)
	done := make(chan bool)
	defer close(done)
	openAccess := func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		select {
		case ret <- nil:
		case <-done:
		}
		return nil, errors.New("ctfproxy: returned")
	}
	denyAccess := func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var code int = http.StatusForbidden
		var title string = "403 Forbidden"
		var description string = "Contact course staff if you believe you should have access"
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "code?", &code, "title?", &title, "description?", &description); err != nil {
			return nil, err
		}
		select {
		case ret <- NewUPError(code, title, description, "", "denyAccess() called in "+servicename+"_access.star call stack:\n"+thread.CallStack().String()):
		case <-done:
		}
		return nil, errors.New("ctfproxy: returned")
	}
	grantAccess := func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if username == "anonymous@anonymous."+_configuration.CorpDomain {
			return denyAccess(thread, b, args, kwargs)
		}
		return openAccess(thread, b, args, kwargs)
	}
	sgroups := starlark.NewList(nil)
	for _, group := range groups {
		sgroups.Append(starlark.String(group))
	}
	predeclared := starlark.StringDict{
		"host":        starlark.String(req.Host),
		"method":      starlark.String(req.Method),
		"path":        starlark.String(req.URL.Path),
		"rawpath":     starlark.String(req.URL.EscapedPath()),
		"query":       starlark.String(req.URL.RawQuery),
		"ip":          starlark.String(req.RemoteAddr),
		"user":        starlark.String(username),
		"corpDomain":  starlark.String(_configuration.CorpDomain),
		"groups":      sgroups,
		"timestamp":   starlark.MakeInt64(time.Now().Unix()),
		"grantAccess": starlark.NewBuiltin("grantAccess", grantAccess),
		"openAccess":  starlark.NewBuiltin("openAccess", openAccess),
		"denyAccess":  starlark.NewBuiltin("denyAccess", denyAccess),
	}
	go func() {
		_, err := starlark.ExecFile(thread, servicename+"_access.star", code, predeclared)
		e := NewUPError(http.StatusForbidden, "403 Forbidden", "Contact course staff if you believe you should have access", "", servicename+"_access.star returned without granting access, default denial")
		if err != nil {
			if err.Error() == "ctfproxy: returned" {
				return
			}
			estr := err.Error()
			if evalerr, ok := err.(*starlark.EvalError); ok {
				estr = evalerr.Backtrace()
			}
			e = NewUPError(http.StatusInternalServerError, "Error happened while determining access rights", "contact course staff if you believe this shouldn't happen", "", estr)
		}
		select {
		case ret <- e:
		case <-done:
		}
	}()

	select {
	case e := <-ret:
		return e
	case <-time.After(1 * time.Second):
		return NewUPError(http.StatusInternalServerError, "Error happened while determining access rights", "contact course staff if you believe this shouldn't happen", "", servicename+"_access.star timed out during evaluation")
	}
}