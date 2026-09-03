package topology

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CTP compatibility, in both directions.
//
// `cubrid-testkit` inherits CTP's `ha_repl` task and CTP's external surface is
// frozen while the old system keeps running, so the conf file is not ours to
// change: `bin/ctp.sh ha_repl -c conf/ha_repl.conf`, with node addresses under
// `env.<instance>.{master,slave}.ssh.*` and engine parameters under
// `env.<instance>.{cubrid,ha,broker1,broker2}.*`.
//
// **The key names survive and the transport does not.** csb runs no sshd and
// publishes no port; testkit's own ADR-014 already demotes `exec.SSH` to one
// implementation of a `Channel`, so the fragment below fills the frozen ssh keys
// with what a docker-exec channel needs -- a container name and the user the
// nodes run as -- and says so in a comment rather than pretending to be a host.

// CTPKey is one `env.<instance>.<section>.<key>` from a CTP conf.
type CTPKey struct {
	Instance string
	Section  string // master | slave | slaveN | cubrid | ha | broker1 | broker2 | cm | brokercommon
	Key      string
	Value    string
}

// ParseCTPConf reads the subset of a CTP conf this tool can honour, and reports
// the rest by name rather than ignoring it.
//
// `default.<section>.<key>` is accepted as an instance-less form, which is what
// CTP calls the values that apply to every node.
func ParseCTPConf(r io.Reader) (keys []CTPKey, other map[string]string, err error) {
	other = map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		name, value := strings.TrimSpace(line[:eq]), strings.TrimSpace(line[eq+1:])
		parts := strings.SplitN(name, ".", 3)
		switch {
		case len(parts) == 3 && parts[0] == "env":
			// env.<instance>.<section>.<key> -- the section and key are the rest
			rest := strings.SplitN(parts[2], ".", 2)
			if len(rest) != 2 {
				other[name] = value
				continue
			}
			keys = append(keys, CTPKey{Instance: parts[1], Section: rest[0], Key: rest[1], Value: value})
		case len(parts) >= 2 && parts[0] == "default":
			rest := strings.SplitN(name[len("default."):], ".", 2)
			if len(rest) != 2 {
				other[name] = value
				continue
			}
			keys = append(keys, CTPKey{Section: rest[0], Key: rest[1], Value: value})
		default:
			other[name] = value
		}
	}
	return keys, other, sc.Err()
}

// CTPSets turns a parsed conf into the --set arguments a create would take, and
// names what it refused.
//
// Validation is kept rather than waived: an unknown key is refused here exactly
// as it is on the command line, because the engine accepts a file with a key it
// ignores and the divergence is then silent (§5). A typo carried over from a CTP
// conf is worth finding at the moment of the move.
// measuredHidden are parameters the engine has and does not advertise: absent
// from `paramdump`, present in the field's own tuning, and measured by this
// project (findings/switchover-threshold.md). They are not typos and refusing
// them as unknown would be a lie, so they route to --set-hidden instead --
// which is what a person would have had to do by hand.
var measuredHidden = map[string]bool{
	"ha_max_heartbeat_gap":            true,
	"ha_heartbeat_interval_in_msecs":  true,
	"ha_calc_score_interval_in_msecs": true,
}

func CTPSets(keys []CTPKey) (sets, hidden, refused []string) {
	for _, k := range keys {
		switch k.Section {
		case "cubrid", "ha":
			// Which file it belongs to is decided by the key, not by the
			// section: CTP's sections say where CTP would have written it, and
			// this model routes by name (§5).
			switch {
			case haKeys[k.Key] || commonKeys[k.Key]:
				sets = append(sets, k.Key+"="+k.Value)
			case measuredHidden[k.Key]:
				hidden = append(hidden, k.Key+"="+k.Value)
			default:
				refused = append(refused, fmt.Sprintf("%s.%s (not a parameter this tool knows)", k.Section, k.Key))
			}
		case "master", "slave", "brokercommon", "broker1", "broker2", "cm":
			// Addresses and broker/manager sections are not engine parameters.
			// The first two are the topology itself and the rest are files this
			// tool writes by construction.
		default:
			if strings.HasPrefix(k.Section, "slave") {
				continue
			}
			refused = append(refused, k.Section+"."+k.Key+" (unknown section)")
		}
	}
	sort.Strings(sets)
	sort.Strings(hidden)
	sort.Strings(refused)
	return sets, hidden, refused
}

// WriteCTPConf emits the `ha_repl.conf` fragment for a standing cluster.
//
// The instance name defaults to the cluster's, since CTP logs it as reference
// information and a name that says which cluster it was is more use than
// `instance1`.
func (t *Topology) WriteCTPConf(w io.Writer, instance, user string) error {
	if instance == "" {
		instance = t.Cluster
	}
	var master string
	var slaves []string
	for _, n := range t.Nodes {
		if n.Role == "master" && master == "" {
			master = n.Name
			continue
		}
		slaves = append(slaves, n.Name)
	}
	if master == "" || len(slaves) == 0 {
		return fmt.Errorf("a CTP ha_repl conf needs a master and at least one slave; this topology has %d node(s)", len(t.Nodes))
	}

	fmt.Fprintf(w, "# ha_repl fragment for the csb cluster %q, written by csb.\n", t.Cluster)
	fmt.Fprintf(w, "# The ssh.host values are CONTAINER NAMES, not hosts: these nodes run no\n")
	fmt.Fprintf(w, "# sshd and publish no port. Reach them with a docker-exec Channel --\n")
	fmt.Fprintf(w, "#     docker exec <ssh.host> bash -lc '<command>'\n")
	fmt.Fprintf(w, "# with the environment csb sets (CUBRID, CUBRID_DATABASES, PATH,\n")
	fmt.Fprintf(w, "# LD_LIBRARY_PATH). The key names are CTP's frozen surface; the transport\n")
	fmt.Fprintf(w, "# is the part that changed.\n#\n")
	fmt.Fprintf(w, "# The engine is bind-mounted from a build on the host, so\n")
	fmt.Fprintf(w, "# cubrid_download_url does not apply and is deliberately absent.\n\n")

	fmt.Fprintf(w, "env.%s.master.ssh.host=%s\n", instance, master)
	fmt.Fprintf(w, "env.%s.master.ssh.user=%s\n", instance, user)
	for i, s := range slaves {
		name := "slave"
		if len(slaves) > 1 {
			name = fmt.Sprintf("slave%d", i+1)
		}
		fmt.Fprintf(w, "env.%s.%s.ssh.host=%s\n", instance, name, s)
		fmt.Fprintf(w, "env.%s.%s.ssh.user=%s\n", instance, name, user)
	}
	if t.DB != "" {
		fmt.Fprintf(w, "\n# the database csb created\nenv.%s.ha.ha_db_list=%s\n", instance, t.DB)
	}
	for _, k := range ctpSortedKeys(t.Parameters.Common) {
		fmt.Fprintf(w, "env.%s.cubrid.%s=%s\n", instance, k, t.Parameters.Common[k])
	}
	for _, k := range ctpSortedKeys(t.Parameters.HA) {
		fmt.Fprintf(w, "env.%s.ha.%s=%s\n", instance, k, t.Parameters.HA[k])
	}
	if len(t.Parameters.Hidden) > 0 {
		fmt.Fprintf(w, "\n# carried unvalidated by --set-hidden; CTP will write them as given\n")
		for _, k := range ctpSortedKeys(t.Parameters.Hidden) {
			fmt.Fprintf(w, "env.%s.ha.%s=%s\n", instance, k, t.Parameters.Hidden[k])
		}
	}
	return nil
}

func ctpSortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
