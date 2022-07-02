package vcs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const defaultRef = "master"

var headBranchRegexp = regexp.MustCompile(`HEAD branch: (?P<branch>.+)`)

func init() {
	Register(newGit, "git")
}

type GitDriver struct {
	DetectRef     bool   `json:"detect-ref"`
	Ref           string `json:"ref"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	refDetetector refDetetector
}

type refDetetector interface {
	detectRef(dir string) string
}

type headBranchDetector struct {
}

func newGit(b []byte) (Driver, error) {
	var d GitDriver

	if b != nil {
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, err
		}
	}

	d.refDetetector = &headBranchDetector{}

	return &d, nil
}

func (g *GitDriver) HeadRev(dir string) (string, error) {
	cmd := exec.Command(
		"git",
		"rev-parse",
		"HEAD")
	cmd.Dir = dir
	r, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	defer r.Close()

	if err := cmd.Start(); err != nil {
		return "", err
	}

	var buf bytes.Buffer

	if _, err := io.Copy(&buf, r); err != nil {
		return "", err
	}

	return strings.TrimSpace(buf.String()), cmd.Wait()
}

func run(desc, dir, cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		log.Printf(
			"Failed to %s %s, see output below\n%sContinuing...",
			desc,
			dir,
			out)
	}

	return string(out), nil
}

func (g *GitDriver) Pull(dir string) (string, error) {
	targetRef := g.targetRef(dir)

	if _, err := run("git fetch", dir,
		"git",
		"fetch",
		"--prune",
		"--no-tags",
		"--depth", "1",
		"origin",
		fmt.Sprintf("+%s:remotes/origin/%s", targetRef, targetRef)); err != nil {
		return "", err
	}

	if _, err := run("git reset", dir,
		"git",
		"reset",
		"--hard",
		fmt.Sprintf("origin/%s", targetRef)); err != nil {
		return "", err
	}

	return g.HeadRev(dir)
}

func (g *GitDriver) targetRef(dir string) string {
	var targetRef string
	if g.Ref != "" {
		targetRef = g.Ref
	} else if g.DetectRef {
		targetRef = g.refDetetector.detectRef(dir)
	}

	if targetRef == "" {
		targetRef = defaultRef
	}

	return targetRef
}

// Create a file with credentials on the format
// git-credential-store expects
func (g *GitDriver) authFile(
	dir string, repoName string, gitPath string,
) (string, error) {
	fName := filepath.Join(dir, "."+repoName+"-credentials")
	u, err := url.Parse(gitPath)
	if err != nil {
		return "", err
	}
	if !exists(fName) {
		f, err := os.Create(fName)
		if err != nil {
			return "", err
		}
		credential := fmt.Sprintf(
			"%s://%s:%s@%s\n", u.Scheme, g.Username, g.Password, u.Host)
		if _, err := f.WriteString(credential); err != nil {
			return "", err
		}
		if err := f.Chmod(0600); err != nil {
			return "", err
		}
	}
	return fName, nil
}

func (g *GitDriver) Clone(dir, url string) (string, error) {
	par, rep := filepath.Split(dir)
	cmdOpts := []string{"clone", "--depth", "1", url, rep}
	if g.Username != "" && g.Password != "" {
		fName, err := g.authFile(par, rep, url)
		if err != nil {
			log.Printf("Failed to setup git credential helper: %v", err)
		} else {
			cmdOpts = append(
				cmdOpts, "--config", "credential.helper=store --file "+fName)
		}
	}
	cmd := exec.Command("git", cmdOpts...)
	cmd.Dir = par
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to clone %s, see output below\n%sContinuing...", url, out)
		return "", err
	}

	return g.Pull(dir)
}

func (g *GitDriver) SpecialFiles() []string {
	return []string{
		".git",
	}
}

func (d *headBranchDetector) detectRef(dir string) string {
	output, err := run("git show remote info", dir,
		"git",
		"remote",
		"show",
		"origin",
	)

	if err != nil {
		log.Printf(
			"error occured when fetching info to determine target ref in %s: %s. Will fall back to default ref %s",
			dir,
			err,
			defaultRef,
		)
		return ""
	}

	matches := headBranchRegexp.FindStringSubmatch(output)
	if len(matches) != 2 {
		log.Printf(
			"could not determine target ref in %s. Will fall back to default ref %s",
			dir,
			defaultRef,
		)
		return ""
	}

	return matches[1]
}
