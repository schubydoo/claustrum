package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cleanOne runs cleanGitChildEnv on one entry and returns what is left of it:
// the whole entry, or "" when it went.
func cleanOne(kv string, form gitEnvForm) string {
	out := cleanGitChildEnv([]string{kv}, form)
	if len(out) == 0 {
		return ""
	}
	return out[0]
}

// The forms of the three systems. The rules run on every host.
var (
	linuxForm   = gitEnvForm{}
	macForm     = gitEnvForm{darwin: true}
	windowsForm = gitEnvForm{windows: true}
)

// The rows of Linux and macOS, rounds task004-01 and task004-02 (Linux and macOS
// VMs). W is the folder of the logging wrapper there.
func TestCleanGitChildEnvUnixRows(t *testing.T) {
	const w = "/home/u/t4/W"
	for _, tc := range []struct{ in, want string }{
		{"PATH=.::rel/bin:" + w + `:/usr/bin:/bin:/usr/bin:/a;b:/c"d`, "PATH=" + w + ":/usr/bin:/bin:/usr/bin"},
		{"PATH=" + w + ":/usr/bin:/bad\ndir:/bin", "PATH=" + w + ":/usr/bin:/bin"},
		{"LD_LIBRARY_PATH=rel:/usr/lib:", "LD_LIBRARY_PATH=/usr/lib"},
		{"LD_LIBRARY_PATH=/a:/b", "LD_LIBRARY_PATH=/a:/b"},
		{"DYLD_LIBRARY_PATH=/abs/x:rel/y", "DYLD_LIBRARY_PATH=/abs/x"},
		{"DYLD_INSERT_LIBRARIES=/abs/a.dylib:rel/b.dylib", "DYLD_INSERT_LIBRARIES=/abs/a.dylib"},
		{"DYLD_FALLBACK_LIBRARY_PATH=/abs/z::", "DYLD_FALLBACK_LIBRARY_PATH=/abs/z"},
		{"GIT_EXEC_PATH=rel/exec", ""},
		{"GIT_EXEC_PATH=", ""},
		{"GIT_EXEC_PATH", ""},
		{"DYLD_FRAMEWORK_PATH=rel", ""},
		{"DYLD_FALLBACK_FRAMEWORK_PATH=.", ""},
		{"path=rel:/abs", "path=rel:/abs"},
		{"LD_PRELOAD=./nonexist.so libdoesnotexist.so", "LD_PRELOAD=libdoesnotexist.so"},
		{"LD_PRELOAD=/abs/a.so:libb.so c/d.so libe.so", "LD_PRELOAD=/abs/a.so:libb.so:libe.so"},
		{"LD_AUDIT=libx.so liby.so:/abs/z.so", "LD_AUDIT=libx.so liby.so:/abs/z.so"},
		{"LD_AUDIT=a/b.so", ""},
		{"GIT_SSH=/abs/ssh", "GIT_SSH=/abs/ssh"},
		{"GIT_SSH=ssh", "GIT_SSH=ssh"},
		{"GIT_SSH=", "GIT_SSH="},
		{"GIT_SSH=bin/ssh", ""},
		{`GIT_SSH=bin\ssh`, ""},
		{"MARKER_KEEP=rel:./x", "MARKER_KEEP=rel:./x"},
		{"GIT_SSH_COMMAND=rel/ssh", "GIT_SSH_COMMAND=rel/ssh"},
		{"GIT_FOO=x", "GIT_FOO=x"},
	} {
		if got := cleanOne(tc.in, linuxForm); got != tc.want {
			t.Errorf("Linux and macOS: %q gives %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The rows of Windows, rounds task004-01 and task004-02 (Windows VM). This test
// runs the rules of Windows on every system.
func TestCleanGitChildEnvWindowsRows(t *testing.T) {
	const w = `C:\o\wrap`
	seven := strings.Join([]string{
		`C:\WINDOWS\system32`, `C:\WINDOWS`, `C:\WINDOWS\System32\Wbem`,
		`C:\WINDOWS\System32\WindowsPowerShell\v1.0\`, `C:\WINDOWS\System32\OpenSSH\`,
		`C:\Program Files\Git\cmd`, `C:\Users\u\AppData\Local\Microsoft\WindowsApps`,
	}, ";")
	for _, tc := range []struct{ in, want string }{
		{`Path=` + w + `;relative\bin;;\nodrive;C:x;C:\a"b;` + seven, `Path=` + w},
		{`Path=` + w + `;C:\abs1;C:\a"b;` + seven, `Path=` + w + `;C:\abs1`},
		{`Path=` + w + `;"C:\x;y";` + seven, `Path=` + w + `;` + seven},
		{`Path=` + w + `;"C:\p q";` + seven, `Path=` + w + `;C:\p q;` + seven},
		{`Path=` + w + `;C:\a"b"c;` + seven, `Path=` + w + `;C:\abc;` + seven},
		{`Path=` + w + `;` + seven, `Path=` + w + `;` + seven},
		{`pAtH=` + w + `;rel;` + seven, `pAtH=` + w + `;` + seven},
		{`GIT_EXEC_PATH=rel\exec`, ""},
		{`DYLD_LIBRARY_PATH=C:\abs;rel`, `DYLD_LIBRARY_PATH=C:\abs`},
		{"LD_PRELOAD=./x.so libok.dll", "LD_PRELOAD=libok.dll"},
		{"LD_PRELOAD=/abs/a.so:libb.so c/d.so libe.so", "LD_PRELOAD=libb.so:libe.so"},
		{`LD_AUDIT=C:\abs\a.dll`, `LD_AUDIT=C:\abs\a.dll`},
		{"LD_AUDIT=libx.so liby.so:/abs/z.so", "LD_AUDIT=libx.so liby.so"},
		{"GIT_SSH=/abs/ssh", ""},
		{`GIT_SSH=C:\abs\ssh.exe`, `GIT_SSH=C:\abs\ssh.exe`},
		{"GIT_SSH=ssh", "GIT_SSH=ssh"},
		{"GIT_SSH=", "GIT_SSH="},
		{`GIT_SSH=bin\ssh`, ""},
		{`MARKER_KEEP=rel;.\x`, `MARKER_KEEP=rel;.\x`},
	} {
		if got := cleanOne(tc.in, windowsForm); got != tc.want {
			t.Errorf("Windows: %q gives %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A table of rows for one system: the entry in, the entry out ("" when it goes).
func checkRows(t *testing.T, form gitEnvForm, label string, rows [][2]string) {
	t.Helper()
	for _, r := range rows {
		if got := cleanOne(r[0], form); got != r[1] {
			t.Errorf("%s: %q gives %q, want %q", label, r[0], got, r[1])
		}
	}
}

// Build c8e2f083 (not measured): no '=' goes, a
// Windows name matches in any letter case and keeps its spelling, and the
// pathspec names go.
func TestGitChildEnvNameRules(t *testing.T) {
	checkRows(t, linuxForm, "Linux", [][2]string{
		{"NOEQUALS", ""},
		{"GIT_LITERAL_PATHSPECS=1", ""},
		{"GIT_GLOB_PATHSPECS=1", ""},
		{"GIT_NOGLOB_PATHSPECS=1", ""},
		{"GIT_ICASE_PATHSPECS=1", ""},
		{"git_literal_pathspecs=1", "git_literal_pathspecs=1"},
		{"Home=rel", "Home=rel"},
		{"=C:=C:\\x", "=C:=C:\\x"},
	})
	checkRows(t, windowsForm, "Windows", [][2]string{
		{"NOEQUALS", ""},
		{"git_literal_pathspecs=1", ""},
		{"Git_Icase_Pathspecs=1", ""},
		{"Home=rel", ""},
		{"Home=C:\\u", "Home=C:\\u"},
		{"git_ssh=bin/ssh", ""},
		{"=C:=C:\\x", "=C:=C:\\x"},
	})
}

// On Windows the exact value /dev/null keeps GIT_CONFIG_GLOBAL and
// GIT_CONFIG_SYSTEM at once. A relative value drops the 23
// names, an absolute or empty value goes on.
func TestGitChildEnvRelativeValues(t *testing.T) {
	checkRows(t, windowsForm, "Windows /dev/null", [][2]string{
		{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null"},
		{"git_config_system=/dev/null", "git_config_system=/dev/null"},
		{"GIT_CONFIG_GLOBAL=/DEV/NULL", ""},
		{"GIT_CONFIG_GLOBAL=nul", "GIT_CONFIG_GLOBAL=nul"},
		{`GIT_CONFIG_GLOBAL=\\h\s\x`, `GIT_CONFIG_GLOBAL=\\h\s\x`},
		{`HOME=\x`, ""},
		{"HOME=/home/x", ""},
		{`HOME=C:\u`, `HOME=C:\u`},
		{"HOME=C:/u", "HOME=C:/u"},
	})
	checkRows(t, linuxForm, "Linux /dev/null", [][2]string{
		{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null"},
		{"GIT_CONFIG_GLOBAL=rel", ""},
		{"HOME=/home/x", "HOME=/home/x"},
		{"HOME=", "HOME="},
	})
	for _, name := range []string{
		"HOME", "USERPROFILE", "PROGRAMDATA", "ALLUSERSPROFILE", "XDG_CACHE_HOME", "XDG_CONFIG_HOME",
		"KRB5_TRACE", "KRB5RCACHEDIR", "GSS_MECH_CONFIG", "OPENSSL_CONF", "OPENSSL_CONF_INCLUDE",
		"OPENSSL_ENGINES", "OPENSSL_MODULES", "SSLKEYLOGFILE", "SSL_CERT_FILE", "CURL_CA_BUNDLE",
		"GIT_INDEX_FILE", "GIT_SSL_CAINFO", "GIT_PROXY_SSL_CAINFO", "GIT_TEMPLATE_DIR",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_TEXTDOMAINDIR",
	} {
		checkRows(t, linuxForm, "Linux relative value", [][2]string{
			{name + "=rel/x", ""}, {name + "=/abs/x", name + "=/abs/x"}, {name + "=", name + "="},
		})
		checkRows(t, windowsForm, "Windows relative value", [][2]string{
			{name + `=rel\x`, ""}, {name + `=C:\abs`, name + `=C:\abs`},
		})
	}
}

// The file part of a Kerberos value.
func TestGitChildEnvKerberos(t *testing.T) {
	var rows [][2]string
	for _, v := range []string{"/abs/cc", "FILE:/abs/cc", "KEYRING:persistent:1000", "kcm:", "API:x", "MEMORY:rel",
		"XCACHE:r", "xctemp:r", "DIR::/a", "DIR:/a", "", "/a:b"} {
		rows = append(rows, [2]string{"KRB5CCNAME=" + v, "KRB5CCNAME=" + v})
	}
	for _, v := range []string{"FILE:rel/cc", "WRFILE:x", ":x", "DIR::rel", "DIR:rel", "rel/cc"} {
		rows = append(rows, [2]string{"KRB5_KTNAME=" + v, ""}, [2]string{"KRB5_CLIENT_KTNAME=" + v, ""})
	}
	rows = append(rows, [2]string{"KRB5CCNAME=a/b:c", ""})
	checkRows(t, linuxForm, "Linux", rows[:len(rows)-1])
	checkRows(t, linuxForm, "Linux", rows[len(rows)-1:])
	checkRows(t, windowsForm, "Windows", [][2]string{
		{`KRB5CCNAME=C:\x`, `KRB5CCNAME=C:\x`},
		{`KRB5CCNAME=FILE:C:\x`, `KRB5CCNAME=FILE:C:\x`},
		{`KRB5CCNAME=FILE:rel`, ""},
		{`krb5ccname=KEYRING:x`, `krb5ccname=KEYRING:x`},
	})
}

// The empty home names, SSH_AUTH_SOCK, HOMEDRIVE and HOMEPATH.
func TestGitChildEnvHomeAndSocket(t *testing.T) {
	var win [][2]string
	for _, n := range []string{"HOME", "USERPROFILE", "PROGRAMDATA", "ALLUSERSPROFILE"} {
		win = append(win, [2]string{n + "=", ""})
	}
	win = append(win,
		[2]string{`SSH_AUTH_SOCK=\\.\pipe\openssh-ssh-agent`, `SSH_AUTH_SOCK=\\.\pipe\openssh-ssh-agent`},
		[2]string{`SSH_AUTH_SOCK=\x`, `SSH_AUTH_SOCK=\x`},
		[2]string{`SSH_AUTH_SOCK=rel`, ""},
		[2]string{`SSH_AUTH_SOCK=`, `SSH_AUTH_SOCK=`},
		[2]string{`HOMEDRIVE=`, ""},
		[2]string{`HOMEDRIVE=C:`, `HOMEDRIVE=C:`},
		[2]string{`HOMEDRIVE=\\server\share`, `HOMEDRIVE=\\server\share`},
		[2]string{`HOMEDRIVE=C:\`, ""},
		[2]string{`HOMEDRIVE=C:\x`, ""},
		[2]string{`HOMEDRIVE=x`, ""},
		[2]string{`homedrive=x`, ""},
		[2]string{`HOMEPATH=`, ""},
		[2]string{`HOMEPATH=\Users\u`, `HOMEPATH=\Users\u`},
		[2]string{`HomePath=/Users/u`, `HomePath=/Users/u`},
		[2]string{`HOMEPATH=Users`, ""},
	)
	checkRows(t, windowsForm, "Windows", win)
	checkRows(t, linuxForm, "Linux", [][2]string{
		{"SSH_AUTH_SOCK=/tmp/agent", "SSH_AUTH_SOCK=/tmp/agent"},
		{"SSH_AUTH_SOCK=agent", ""},
		{"SSH_AUTH_SOCK=", "SSH_AUTH_SOCK="},
		{"HOMEDRIVE=x", "HOMEDRIVE=x"},
		{"HOMEPATH=", "HOMEPATH="},
		{"USERPROFILE=", "USERPROFILE="},
	})
}

// Five names keep an empty value, and the 19 list names
// follow the list rule.
func TestGitChildEnvLists(t *testing.T) {
	for _, n := range []string{"SSL_CERT_DIR", "GIT_SSL_CAPATH", "KRB5_CONFIG", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "SASL_PATH"} {
		for _, f := range []gitEnvForm{linuxForm, macForm, windowsForm} {
			checkRows(t, f, "empty value", [][2]string{{n + "=", n + "="}, {n + "=rel", ""}})
		}
	}
	for _, n := range []string{
		"PATH", "LOCPATH", "NLSPATH", "GCONV_PATH", "LD_LIBRARY_PATH", "SASL_PATH", "SASL_CONF_PATH",
		"KRB5_CONFIG", "SSL_CERT_DIR", "GIT_EXEC_PATH", "GIT_SSL_CAPATH", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"DYLD_LIBRARY_PATH", "DYLD_FRAMEWORK_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_FALLBACK_LIBRARY_PATH",
		"DYLD_FALLBACK_FRAMEWORK_PATH", "DYLD_VERSIONED_LIBRARY_PATH", "DYLD_VERSIONED_FRAMEWORK_PATH",
	} {
		checkRows(t, linuxForm, "Linux "+n, [][2]string{
			{n + "=rel:/a::/b", n + "=/a:/b"}, {n + "=rel", ""}, {n + "=/a;b:/c", n + "=/c"},
		})
		checkRows(t, windowsForm, "Windows "+n, [][2]string{
			{n + `=rel;C:\a;\\h\s\b;C:\c:d`, n + `=C:\a;\\h\s\b;C:\c:d`}, {n + `=rel`, ""},
		})
	}
	checkRows(t, windowsForm, "Windows", [][2]string{{"Path=;", ""}, {"Path=", ""}})
}

// LD_PRELOAD splits on white space too, LD_AUDIT on ':' only.
func TestGitChildEnvLoaders(t *testing.T) {
	checkRows(t, linuxForm, "Linux", [][2]string{
		{"LD_PRELOAD=a.so\tb.so\nc.so\vd.so\fe.so\rf.so", "LD_PRELOAD=a.so:b.so:c.so:d.so:e.so:f.so"},
		{"LD_PRELOAD=x/a.so\ty.so", "LD_PRELOAD=y.so"},
		{"LD_PRELOAD=x/a.so\ty/b.so", ""},
		{"LD_AUDIT=a.so\tb.so", "LD_AUDIT=a.so\tb.so"},
		{"LD_AUDIT=x/a.so\tb", ""},
	})
	checkRows(t, windowsForm, "Windows", [][2]string{
		{"LD_PRELOAD=a.so\tb.so", "LD_PRELOAD=a.so:b.so"},
		{"LD_PRELOAD=\\\\h\\s\\x.dll", "LD_PRELOAD=\\\\h\\s\\x.dll"},
	})
}

// The program names, and SSH_SK_PROVIDER on macOS and Windows.
func TestGitChildEnvPrograms(t *testing.T) {
	for _, n := range []string{"GIT_SSH", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_SK_HELPER", "SSH_SK_PROVIDER", "SSH_PKCS11_HELPER"} {
		checkRows(t, linuxForm, "Linux "+n, [][2]string{
			{n + "=/abs/p", n + "=/abs/p"}, {n + "=bare", n + "=bare"}, {n + "=", n + "="},
			{n + "=a/p", ""}, {n + `=a\p`, ""}, {n + `=C:x`, n + `=C:x`},
		})
		bare := n + "=bare"
		if n == "SSH_SK_PROVIDER" {
			bare = "" // SSH_SK_PROVIDER on Windows
		}
		checkRows(t, windowsForm, "Windows "+n, [][2]string{
			{n + `=C:\p`, n + `=C:\p`}, {n + `=\\h\s\p`, n + `=\\h\s\p`}, {n + "=bare", bare},
			{n + "=a/p", ""}, {n + `=a\p`, ""}, {n + `=C:x`, ""}, {n + "=/abs/p", ""},
		})
	}
	checkRows(t, linuxForm, "Linux SSH_SK_PROVIDER", [][2]string{{"SSH_SK_PROVIDER=libsk.so", "SSH_SK_PROVIDER=libsk.so"}})
	checkRows(t, macForm, "macOS SSH_SK_PROVIDER", [][2]string{
		{"SSH_SK_PROVIDER=internal", "SSH_SK_PROVIDER=internal"},
		{"SSH_SK_PROVIDER=/usr/lib/sk.dylib", "SSH_SK_PROVIDER=/usr/lib/sk.dylib"},
		{"SSH_SK_PROVIDER=libsk.so", ""},
		{"SSH_SK_PROVIDER=", ""},
		{"SSH_ASKPASS=", "SSH_ASKPASS="},
	})
	checkRows(t, windowsForm, "Windows SSH_SK_PROVIDER", [][2]string{
		{"SSH_SK_PROVIDER=internal", "SSH_SK_PROVIDER=internal"},
		{`SSH_SK_PROVIDER=C:\sk.dll`, `SSH_SK_PROVIDER=C:\sk.dll`},
		{"SSH_SK_PROVIDER=libsk.dll", ""},
		{"SSH_SK_PROVIDER=", ""},
	})
}

// An entry that no test touches stays as it is. An empty name on
// Windows is not measured.
func TestGitChildEnvOthersStay(t *testing.T) {
	for _, f := range []gitEnvForm{linuxForm, macForm, windowsForm} {
		checkRows(t, f, "other entries", [][2]string{
			{"CLAUSTRUM_X=rel:;\"", "CLAUSTRUM_X=rel:;\""}, {"CLAUSTRUM_Y=", "CLAUSTRUM_Y="}, {"GIT_FOO=rel", "GIT_FOO=rel"},
		})
	}
}

// Every name keeps its place, and a name that goes leaves the others in order.
func TestCleanGitChildEnvKeepsOrder(t *testing.T) {
	in := []string{"A=1", "PATH=rel:/usr/bin", "GIT_SSH=bin/ssh", "B=2", "LD_AUDIT=/a.so", "C=3"}
	want := []string{"A=1", "PATH=/usr/bin", "B=2", "LD_AUDIT=/a.so", "C=3"}
	if got := cleanGitChildEnv(in, linuxForm); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The env builders of class H calls clean the daemon's environment, and the
// builders of the excludes read and the version calls do not. The names here
// are removed on every system and start no child.
func TestGitChildEnvReachesTheBuilders(t *testing.T) {
	t.Setenv("GIT_EXEC_PATH", "rel/exec")
	t.Setenv("DYLD_FRAMEWORK_PATH", "rel")
	t.Setenv("GIT_SSH", "bin/ssh")
	t.Setenv("CLAUSTRUM_MARKER_KEEP", "rel:./x")
	gone := []string{"GIT_EXEC_PATH", "DYLD_FRAMEWORK_PATH", "GIT_SSH"}
	for _, b := range []struct {
		name string
		env  []string
	}{
		{"hardenedGitEnv", hardenedGitEnv(true, nil, nil)},
		{"gitUse.hardenedGitEnv", gitUse{}.hardenedGitEnv(false, nil, nil)},
		{"branchStepEnv", branchStepEnv(nil, nil)},
		{"precursorEnv", precursorEnv(false, nil)},
		{"gitUse.precursorEnv", gitUse{}.precursorEnv(true, nil)},
		{"repoConfigListEnv", repoConfigListEnv()},
		{"probeEnv", probeEnv(t.TempDir(), filepath.Join(t.TempDir(), "repo"))},
		{"shadowTestEnv", shadowTestEnv()},
	} {
		for _, n := range gone {
			if v, ok := lookupExact(b.env, n); ok {
				t.Errorf("%s keeps %s=%q", b.name, n, v)
			}
		}
		if _, ok := lookupExact(b.env, "CLAUSTRUM_MARKER_KEEP"); !ok {
			t.Errorf("%s lost CLAUSTRUM_MARKER_KEEP", b.name)
		}
	}
	if v, _ := lookupExact(shadowDaemonEnv(), "GIT_EXEC_PATH"); v != "rel/exec" {
		t.Errorf("the plain version calls lost GIT_EXEC_PATH: %q", v)
	}
}

// The calls of the start test get no GIT_ name of the daemon. A class H call
// keeps them. The names that the test sets stay.
func TestShadowTestEnvHasNoDaemonGitNames(t *testing.T) {
	t.Setenv("GIT_FOO", "x")
	t.Setenv("GIT_SSH_COMMAND", "rel/ssh")
	t.Setenv("GIT_TRACE2_EVENT_NESTING", "2")
	t.Setenv("GIT_SSH", "ssh")
	env := shadowTestEnv()
	for _, n := range []string{"GIT_FOO", "GIT_SSH_COMMAND", "GIT_TRACE2_EVENT_NESTING", "GIT_SSH"} {
		if v, ok := lookupExact(env, n); ok {
			t.Errorf("a start test call keeps %s=%q", n, v)
		}
		if _, ok := lookupExact(hardenedGitEnv(true, nil, nil), n); !ok {
			t.Errorf("a class H call lost %s", n)
		}
	}
	if v, _ := lookupExact(env, "GIT_CONFIG_GLOBAL"); v != "/dev/null" {
		t.Errorf("the start test lost its own GIT_CONFIG_GLOBAL: %q", v)
	}
}

// A hardened call reads its answer from stdout only. The stub writes a loader
// line to stderr and runs the real git. git.info then answers the branch of
// stdout, and isRepo sees "true". 5fd08069 answers "branch":"main" and creates
// the worktree there (round task004-01, cells M1 and M4, Linux VM).
func TestHardenedAnswerIsStdoutOnly(t *testing.T) {
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	runGit(t, filepath.Dir(repo), "init", "-q", "-b", "main", repo)
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	installGitSlowStub(t, realGit)
	const line = `ERROR: ld.so: object 'libdoesnotexist.so' from LD_PRELOAD cannot be preloaded (cannot open shared object file): ignored.\n`

	slowGit(t, "branch,--show-current", "pre", 0, line, "")
	s := newTestServer(t)
	if got := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": repo})); !strings.Contains(got, `"branch":"main"`) {
		t.Errorf("git.info = %s, want \"branch\":\"main\"", got)
	}

	slowGit(t, "rev-parse,--is-inside-work-tree", "pre", 0, line, "")
	if ok, _ := isRepo(gitUse{}, repo, configListing{}); !ok {
		t.Error("isRepo read stderr into the answer of rev-parse --is-inside-work-tree")
	}

	slowGit(t, "rev-parse,--absolute-git-dir", "pre", 0, line, "")
	dir, answered, err := worktreeBaseGitDir(gitUse{}, repo)
	want, _ := os.Stat(filepath.Join(repo, ".git"))
	if got, serr := os.Stat(dir); err != nil || !answered || serr != nil || !os.SameFile(got, want) {
		t.Errorf("worktreeBaseGitDir = %q, %v, %v, want the folder %s", dir, answered, err, filepath.Join(repo, ".git"))
	}
}

// Every git call of the five git methods gets the cleaned environment, except the
// excludes read and the version calls, which keep the names as they are. The two
// names here are removed on every system and do no harm to git. git.status reads
// a config.worktree file of the entry, so its own read of that file runs too.
func TestGitMethodsCleanTheChildEnv(t *testing.T) {
	f := newListingFixture(t)
	t.Setenv("GIT_SSH", "bin/ssh")
	t.Setenv("DYLD_FRAMEWORK_PATH", "rel")
	cleaned, raw := 0, 0
	for _, m := range []struct {
		name   string
		params map[string]any
	}{
		{"git.info", map[string]any{"path": f.top}},
		{"git.list_branches", map[string]any{"path": f.top}},
		{"git.worktree_create", map[string]any{"baseRepo": f.top, "worktreePath": f.wt(), "branchName": "w1"}},
		{"git.status", map[string]any{"path": f.wt(), "baseRepo": f.top}},
		{"git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.wt(), "branchName": "w1"}},
	} {
		if m.name == "git.status" {
			writeFile(t, filepath.Join(f.top, ".git", "worktrees", "w1", "config.worktree"), "[core]\n\tsparseCheckout = false\n", 0o644)
		}
		reply, calls := f.call(t, m.name, m.params)
		if strings.Contains(reply, `"error"`) || strings.Contains(reply, `"success":false`) {
			t.Fatalf("%s = %s", m.name, reply)
		}
		for _, c := range calls {
			_, ssh := lookupExact(c.env, "GIT_SSH")
			_, dyld := lookupExact(c.env, "DYLD_FRAMEWORK_PATH")
			keeps := c.excludesRead() || c.argv[len(c.argv)-1] == "version"
			switch {
			case keeps && (!ssh || !dyld):
				t.Errorf("%s: call %q lost a name of the daemon", m.name, c.argv)
			case !keeps && (ssh || dyld):
				t.Errorf("%s: call %q keeps GIT_SSH or DYLD_FRAMEWORK_PATH", m.name, c.argv)
			case keeps:
				raw++
			default:
				cleaned++
			}
		}
	}
	if cleaned == 0 || raw == 0 {
		t.Fatalf("cleaned %d calls and kept %d: the stub saw too little", cleaned, raw)
	}
}

// With "shadowDirOff":true the config.worktree read of git.status has the form of
// the read of a config file: the working directory gitVersionDir, GIT_DIR at the
// null device, LC_ALL=C and LANGUAGE=C. 5fd08069 runs it so (cell V3 of round
// task004-03, Linux, macOS and Windows VMs).
func TestStatusEntryConfigReadWithMember(t *testing.T) {
	f := newListingFixture(t)
	if _, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.top, "worktreePath": f.wt(), "branchName": "w1"}); len(calls) == 0 {
		t.Fatal("the create made no git call")
	}
	writeFile(t, filepath.Join(f.top, ".git", "worktrees", "w1", "config.worktree"), "[core]\n\tsparseCheckout = false\n", 0o644)
	reply, calls := f.call(t, "git.status", map[string]any{"path": f.wt(), "baseRepo": f.top, "shadowDirOff": true})
	if strings.Contains(reply, `"error"`) {
		t.Fatalf("git.status = %s", reply)
	}
	wantDir := gitVersionDir
	if wantDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		wantDir = wd
	}
	seen := false
	for _, c := range calls {
		if !c.configRead() {
			continue
		}
		seen = true
		for name, want := range map[string]string{"GIT_DIR": os.DevNull, "LC_ALL": "C", "LANGUAGE": "C"} {
			if v, ok := lookupExact(c.env, name); !ok || v != want {
				t.Errorf("the config.worktree read has %s=%q (set %v), want %q", name, v, ok, want)
			}
		}
		if a, _ := filepath.EvalSymlinks(c.cwd); a != wantDir {
			if b, _ := filepath.EvalSymlinks(wantDir); a != b {
				t.Errorf("the config.worktree read runs in %q, want %q", c.cwd, wantDir)
			}
		}
	}
	if !seen {
		t.Fatal("git.status made no config.worktree read")
	}
}
