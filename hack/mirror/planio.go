package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const defaultProxy = "https://proxy.golang.org"

var releaseBranchPattern = regexp.MustCompile(`^release-1\.\d+$`)

// proxyURL returns the first real proxy in a GOPROXY value.
func proxyURL(goproxy string) string {
	for _, entry := range strings.FieldsFunc(goproxy, func(r rune) bool { return r == ',' || r == '|' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" || entry == "direct" || entry == "off" {
			continue
		}

		return strings.TrimSuffix(entry, "/")
	}

	return defaultProxy
}

// releaseBranch maps a remote-tracking ref such as origin/release-1.36 to the
// branch name, reporting whether it is a mirrored release line.
func releaseBranch(ref, remote string) (string, bool) {
	name, ok := strings.CutPrefix(ref, remote+"/")
	if !ok || (name != mainName && !releaseBranchPattern.MatchString(name)) {
		return "", false
	}

	return name, true
}

func runPlan(ctx context.Context, repo, remote string, w io.Writer) error {
	versions, err := fetchVersions(ctx, proxyURL(os.Getenv("GOPROXY")))
	if err != nil {
		return err
	}

	branches, err := listBranches(ctx, repo, remote)
	if err != nil {
		return err
	}

	updates, err := Plan(versions, branches)
	if err != nil {
		return err
	}

	if err = json.NewEncoder(w).Encode(struct {
		Include []Update `json:"include"`
	}{updates}); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}

	return nil
}

func fetchVersions(ctx context.Context, proxy string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxy+"/"+upstreamModule+"/@v/list", nil)
	if err != nil {
		return nil, fmt.Errorf("build proxy request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list %s versions: %w", upstreamModule, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read version list: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list %s versions: %s: %s", upstreamModule, resp.Status, body)
	}

	return strings.Fields(string(body)), nil
}

func listBranches(ctx context.Context, repo, remote string) ([]Branch, error) {
	refs, err := gitCmd(ctx, repo, "for-each-ref", "--format=%(refname:short)", "refs/remotes/"+remote+"/")
	if err != nil {
		return nil, err
	}

	var branches []Branch

	for ref := range strings.FieldsSeq(string(refs)) {
		name, ok := releaseBranch(ref, remote)
		if !ok {
			continue
		}

		raw, showErr := gitCmd(ctx, repo, "show", remote+"/"+name+":"+mirrorDir+"/"+manifestName)
		if showErr != nil {
			continue // the branch predates the mirror
		}

		var m manifest
		if err = json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("branch %s: parse %s: %w", name, manifestName, err)
		}

		branches = append(branches, Branch{Name: name, Version: m.Kubernetes})
	}

	return branches, nil
}

func gitCmd(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}

	return out, nil
}
