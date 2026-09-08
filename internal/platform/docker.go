package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Runtime is the boundary a future ECS or VM implementation replaces.
type Runtime interface {
	Ensure(context.Context, Deployment, Revision) ([]Instance, string, error)
	Retire(context.Context, Deployment) error
}
type Docker struct {
	client  *http.Client
	network string
	owner   string
}

func NewDocker(socket, network, owner string) *Docker {
	return &Docker{client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 90 * time.Second}, network: network, owner: owner}
}
func (d *Docker) call(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker/v1.44"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return fmt.Errorf("Docker %s: %s", res.Status, b)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	_, err = io.Copy(io.Discard, res.Body)
	return err
}

type containerInfo struct {
	ID              string `json:"Id"`
	Image           string
	Config          struct{ Labels map[string]string }
	State           struct{ Running bool }
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

func (d *Docker) Ensure(ctx context.Context, dep Deployment, rev Revision) ([]Instance, string, error) {
	image := dep.ImageID
	if image == "" {
		var info struct {
			ID string `json:"Id"`
		}
		err := d.call(ctx, "GET", "/images/"+url.PathEscape(rev.Image)+"/json", nil, &info)
		if err != nil {
			// Pull only explicitly registered images; no host mounts or privileges are exposed.
			if err = d.call(ctx, "POST", "/images/create?fromImage="+url.QueryEscape(rev.Image), nil, nil); err != nil {
				return nil, "", err
			}
			if err = d.call(ctx, "GET", "/images/"+url.PathEscape(rev.Image)+"/json", nil, &info); err != nil {
				return nil, "", err
			}
		}
		image = info.ID
	}
	instances := []Instance{}
	for i := 0; i < dep.Replicas; i++ {
		name := d.owner + "-" + dep.ID + fmt.Sprintf("-%d", i+1)
		var info containerInfo
		if err := d.call(ctx, "GET", "/containers/"+name+"/json", nil, &info); err != nil {
			env := []string{"CELL=" + dep.Cell}
			for k, v := range rev.Env {
				env = append(env, k+"="+v)
			}
			sort.Strings(env)
			body := map[string]any{"Image": image, "Env": env, "Labels": map[string]string{"harbor.owner": d.owner, "harbor.deployment": dep.ID}, "HostConfig": map[string]any{"NetworkMode": d.network, "RestartPolicy": map[string]any{"Name": "unless-stopped"}, "Memory": int64(128 * 1024 * 1024), "NanoCpus": int64(500000000), "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}}, "NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{d.network: map[string]any{}}}}
			if err = d.call(ctx, "POST", "/containers/create?name="+name, body, nil); err != nil {
				return instances, image, err
			}
			if err = d.call(ctx, "GET", "/containers/"+name+"/json", nil, &info); err != nil {
				return instances, image, err
			}
		}
		if info.Config.Labels["harbor.owner"] != d.owner {
			return instances, image, fmt.Errorf("container %s is not owned by this platform", name)
		}
		if !info.State.Running {
			if err := d.call(ctx, "POST", "/containers/"+name+"/start", nil, nil); err != nil {
				return instances, image, err
			}
			if err := d.call(ctx, "GET", "/containers/"+name+"/json", nil, &info); err != nil {
				return instances, image, err
			}
		}
		ip := info.NetworkSettings.Networks[d.network].IPAddress
		instances = append(instances, Instance{Name: name, IP: ip, Ready: probe(ctx, fmt.Sprintf("http://%s:%d%s", ip, rev.Port, rev.HealthPath), "")})
	}
	return instances, image, nil
}
func (d *Docker) Retire(ctx context.Context, dep Deployment) error {
	for i := 0; i < dep.Replicas; i++ {
		name := d.owner + "-" + dep.ID + fmt.Sprintf("-%d", i+1)
		var info containerInfo
		if err := d.call(ctx, "GET", "/containers/"+name+"/json", nil, &info); err != nil {
			if strings.Contains(err.Error(), "404") {
				continue
			}
			return err
		}
		if info.Config.Labels["harbor.owner"] != d.owner {
			return fmt.Errorf("container ownership mismatch")
		}
		if info.State.Running {
			if err := d.call(ctx, "POST", "/containers/"+name+"/stop?t=10", nil, nil); err != nil {
				return err
			}
		}
		if err := d.call(ctx, "DELETE", "/containers/"+name, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

var probeClient = &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

func probe(ctx context.Context, address, host string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return false
	}
	req.Host = host
	res, err := probeClient.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	return res.StatusCode >= 200 && res.StatusCode < 400
}
