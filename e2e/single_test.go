//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/cli"
)

// TestSinglePreset stands up the preset the design has always described as
// "ha_mode=off, one server" and checks that it is one: no heartbeat, a server
// started and stopped by name, `master` resolving to the one node, and every
// HA verb refusing with a sentence rather than an engine error. It runs its
// own cluster so that TestSurface's pair is untouched.
func TestSinglePreset(t *testing.T) {
	build := os.Getenv("CSB_E2E_BUILD")
	if build == "" {
		t.Skip("set CSB_E2E_BUILD to a CUBRID install tree to run the single preset against a real engine")
	}
	bin, err := filepath.Abs("../bin/csb")
	if err != nil || !exists(bin) {
		t.Fatalf("build the binary first (make build): %v", err)
	}
	home := os.Getenv("CSB_E2E_HOME")
	if home == "" {
		home = t.TempDir()
	}
	c := &csb{t: t, bin: bin, home: home,
		cluster: fmt.Sprintf("solo%d", time.Now().Unix()%100000)}
	t.Logf("cluster %s, state under %s", c.cluster, c.home)

	tools := filepath.Join(home, "tools-single")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	// A client image of the user's own: the base image plus a marker file, so
	// the test can tell which node runs which. It is built with the backend
	// csb will use, from a recipe that needs no network beyond ubuntu:24.04.
	clientImage := fmt.Sprintf("csb-e2e-client:%d", time.Now().Unix()%100000)
	buildClientImage(t, clientImage)

	t.Run("a client image that is not here is a precondition", func(t *testing.T) {
		c.t = t
		e := c.wantExit(cli.ExitPrecondition, "cluster", "create", "--name", c.cluster, "--preset", "single", "--build", build,
			"--clients", "1", "--client-image", "csb-e2e-no-such-image:0", "--timeout", "120s")
		if !hasNote(e, "client_image_missing") {
			t.Errorf("want client_image_missing, got %s", notes(e))
		}
		// Nothing was made: the check comes before the network and the nodes,
		// so ls finds no container. (The run record of the refused command is
		// written, as every command's is, so the name may be listed with
		// containers: 0 -- that is the record, not a cluster.)
		if ls, _ := c.run("cluster", "ls", "--timeout", "60s"); ls != nil {
			d, _ := ls.Data.(map[string]any)
			rows, _ := d["clusters"].([]any)
			for _, r := range rows {
				row, _ := r.(map[string]any)
				if row["name"] == c.cluster {
					if n, _ := row["containers"].(float64); n != 0 {
						t.Errorf("a refused create left %v container(s) of %s behind", n, c.cluster)
					}
				}
			}
		}
	})
	defer func() {
		c.t = t
		if t.Failed() && os.Getenv("CSB_E2E_KEEP") != "0" {
			t.Logf("KEEPING cluster %s under %s for inspection; csb cluster destroy --cluster %s --purge",
				c.cluster, c.home, c.cluster)
			return
		}
		if _, code := c.run("cluster", "destroy", "--purge", "--timeout", "300s"); code != cli.ExitOK {
			t.Errorf("destroy exited %d", code)
		}
	}()

	t.Run("create serves without a heartbeat", func(t *testing.T) {
		c.t = t
		// Two --set keys: one the table knows, one it does not. The second is
		// what a measurement cluster has to set and what the refusal used to
		// keep out; the engine accepting it at start is the proof the table
		// was gating something real.
		e, code := c.run("cluster", "create", "--name", c.cluster, "--preset", "single", "--build", build,
			"--with-broker", "--clients", "1", "--tools", tools, "--client-image", clientImage,
			"--set", "log_buffer_size=16M", "--set", "double_write_buffer_size=0", "--timeout", "600s")
		if code != cli.ExitOK {
			t.Fatalf("create exited %d: %s", code, notes(e))
		}
		d, _ := e.Data.(map[string]any)
		if d["state"] != "serving" {
			t.Fatalf("state = %v, want serving", d["state"])
		}
		if !hasNote(e, "unverified_parameter_set") {
			t.Errorf("a --set key outside the table was not reported: %s", notes(e))
		}
		// The artifact records the mode and the keys, and the file the engine
		// reads agrees.
		desc := c.must("cluster", "describe", "--timeout", "60s")
		if desc["ha_mode"] != "off" {
			t.Errorf("describe ha_mode = %v, want off", desc["ha_mode"])
		}
		if params, _ := desc["parameters"].(map[string]any); params != nil {
			if unv, _ := params["unverified"].([]any); len(unv) != 1 || unv[0] != "double_write_buffer_size" {
				t.Errorf("describe parameters.unverified = %v, want [double_write_buffer_size]", unv)
			}
		} else {
			t.Errorf("describe carries no parameters: %v", desc)
		}
		conf, rerr := os.ReadFile(filepath.Join(home, "clusters", c.cluster, "work", c.cluster+"-n1", "cubrid", "conf", "cubrid.conf"))
		if rerr != nil {
			t.Fatalf("cubrid.conf not written: %v", rerr)
		}
		if !strings.Contains(string(conf), "\nha_mode=off\n") || strings.Contains(string(conf), "\nha_mode=on\n") {
			t.Errorf("cubrid.conf does not carry ha_mode=off alone:\n%s", conf)
		}
		for _, want := range []string{"\nlog_buffer_size=16M\n", "\ndouble_write_buffer_size=0\n"} {
			if !strings.Contains(string(conf), want) {
				t.Errorf("cubrid.conf is missing %q", strings.TrimSpace(want))
			}
		}
		for _, n := range c.nodes("ha status") {
			if n["name"] != c.cluster+"-n1" {
				continue
			}
			if n["server_state"] != "standalone" || n["role"] != "standalone" {
				t.Errorf("n1 = %v, want server_state and role standalone", n)
			}
		}
	})
	c.t = t
	if t.Failed() {
		return
	}

	t.Run("master names the one node and slave says why not", func(t *testing.T) {
		c.t = t
		if code := c.exec("master", "csql -u dba -c 'SELECT 1 FROM db_root' "+c.cluster); code != 0 {
			t.Errorf("csql through master exited %d", code)
		}
		if code := c.exec("n1", "csql -u dba -c 'SELECT 1 FROM db_root' "+c.cluster); code != 0 {
			t.Errorf("csql through n1 exited %d", code)
		}
		e, code := c.run("node", "exec", "slave", "--timeout", "60s", "--", "true")
		if code == cli.ExitOK || !strings.Contains(notes(e), "no standby") {
			t.Errorf("slave on a single: exit %d, notes %s; want a refusal that names the preset", code, notes(e))
		}
		// The client reaches the database node across the cluster network.
		if code := c.exec("client", "csql -u dba -c 'SELECT 1 FROM db_root' "+c.cluster+"@"+c.cluster+"-n1"); code != 0 {
			t.Errorf("csql from the client node exited %d", code)
		}
		// And it runs the image it was given; the database node does not.
		if code := c.exec("client", "test -f /client-image-marker"); code != 0 {
			t.Errorf("the client node is not running the client image (exit %d)", code)
		}
		if code := c.exec("n1", "test -f /client-image-marker"); code == 0 {
			t.Error("the database node is running the client image")
		}
	})

	t.Run("the HA verbs refuse by name", func(t *testing.T) {
		c.t = t
		for _, argv := range [][]string{
			{"ha", "promote", "master", "--timeout", "60s"},
			{"ha", "resync", "--timeout", "60s"},
			{"repl", "check", "--timeout", "60s"},
		} {
			e, code := c.run(argv...)
			if code == cli.ExitOK || !hasNote(e, "no_ha_group") {
				t.Errorf("csb %s: exit %d, notes %s; want no_ha_group", strings.Join(argv, " "), code, notes(e))
			}
		}
	})

	t.Run("stop and start by name", func(t *testing.T) {
		c.t = t
		c.must("node", "stop", "master", "--timeout", "120s")
		if st := c.serverState(c.cluster + "-n1"); st != "" {
			t.Errorf("after stop, server_state = %q, want empty", st)
		}
		// master is a query: with the server down it names nothing.
		if _, code := c.run("node", "exec", "master", "--timeout", "60s", "--", "true"); code == cli.ExitOK {
			t.Error("master resolved while the server was stopped")
		}
		c.must("node", "start", "n1", "--timeout", "120s")
		if !c.waitStandalone(60 * time.Second) {
			t.Fatalf("the server did not come back within 60s")
		}
	})

	t.Run("down and up", func(t *testing.T) {
		c.t = t
		c.must("cluster", "down", "--timeout", "120s")
		if st := c.serverState(c.cluster + "-n1"); st != "" {
			t.Errorf("after down, server_state = %q, want empty", st)
		}
		d := c.must("cluster", "up", "--timeout", "300s")
		if d["state"] != "serving" {
			t.Fatalf("up: state = %v, want serving", d["state"])
		}
		if code := c.exec("master", "csql -u dba -c 'SELECT 1 FROM db_root' "+c.cluster); code != 0 {
			t.Errorf("csql after up exited %d", code)
		}
	})
}

// buildClientImage makes a throwaway client image with the backend the suite
// runs against: CSB_BACKEND, else docker.
func buildClientImage(t *testing.T, tag string) {
	t.Helper()
	backend := os.Getenv("CSB_BACKEND")
	if backend == "" {
		backend = "docker"
	}
	cmd := exec.Command(backend, "build", "-q", "-t", tag, "-")
	cmd.Stdin = strings.NewReader("FROM ubuntu:24.04\nRUN touch /client-image-marker\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s build of the client image failed: %v\n%s", backend, err, out)
	}
	t.Cleanup(func() { _ = exec.Command(backend, "rmi", "-f", tag).Run() })
}

// serverState is what `ha status` reports for one node.
func (c *csb) serverState(node string) string {
	c.t.Helper()
	for _, n := range c.nodes("ha status") {
		if n["name"] == node {
			s, _ := n["server_state"].(string)
			return s
		}
	}
	return ""
}

func (c *csb) waitStandalone(d time.Duration) bool {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c.serverState(c.cluster+"-n1") == "standalone" {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}
