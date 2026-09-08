package platform

import (
	"context"
	"fmt"
	"log"
	"time"
)

type Controller struct {
	Store   *Store
	Runtime Runtime
	XDS     *XDS
}

func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		c.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (c *Controller) tick(ctx context.Context) {
	state := c.Store.copy()
	for _, dep := range state.Deployments {
		rev := state.revision(dep.Revision)
		if rev == nil {
			continue
		}
		if dep.Retired {
			if dep.Status != "retired" {
				err := c.Runtime.Retire(ctx, dep)
				c.Store.mu.Lock()
				d := c.Store.State.deployment(dep.ID)
				if err != nil {
					d.Error = err.Error()
				} else {
					d.Status = "retired"
					d.Instances = []Instance{}
					d.Error = ""
					c.Store.event("deployment", d.ID, "Containers retired")
				}
				c.Store.mustSave()
				c.Store.mu.Unlock()
			}
			continue
		}
		// Once any deployment resolves this revision, reuse that exact image for
		// every cell, even if its original registry tag has moved.
		if dep.ImageID == "" {
			for _, other := range state.Deployments {
				if other.Revision == dep.Revision && other.ImageID != "" {
					dep.ImageID = other.ImageID
					break
				}
			}
		}
		instances, image, err := c.Runtime.Ensure(ctx, dep, *rev)
		if image != "" {
			state.deployment(dep.ID).ImageID = image
		}
		c.Store.mu.Lock()
		d := c.Store.State.deployment(dep.ID)
		old := d.Status
		d.Instances = instances
		if image != "" {
			d.ImageID = image
		}
		d.Error = ""
		d.Status = "ready"
		if err != nil {
			d.Error = err.Error()
			d.Status = "error"
		} else if len(instances) != d.Replicas {
			d.Status = "starting"
		} else {
			for _, in := range instances {
				if !in.Ready {
					d.Status = "starting"
				}
			}
		}
		if old != d.Status {
			c.Store.event("deployment", d.ID, "Deployment is "+d.Status)
		}
		c.Store.mustSave()
		c.Store.mu.Unlock()
	}
	state = c.Store.copy()
	if err := c.XDS.Publish(ctx, state); err != nil {
		log.Printf("publish: %v", err)
		return
	}
	c.XDS.check(ctx)
	// Inspect traffic outside the state lock. Each observation is applied only to the
	// same rollout phase, so concurrent operator cancellation wins.
	for _, r := range state.Rollouts {
		if !active(r.Status) || r.Status == "awaiting_approval" {
			continue
		}
		if r.Status == "observing" || r.Status == "reverting" {
			t := state.tenant(r.Tenant)
			if t == nil {
				continue
			}
			if !c.XDS.converged() {
				continue
			}
			ok := true
			for _, g := range []string{"gateway-a", "gateway-b"} {
				if !probe(ctx, "http://"+g+":8080/", t.Domains[0]) {
					ok = false
				}
			}
			c.Store.mu.Lock()
			current := c.findRollout(r.ID)
			if current == nil || current.Status != r.Status {
				c.Store.mu.Unlock()
				continue
			}
			if r.Status == "reverting" {
				if ok {
					current.Status = "rolled_back"
					current.Message = "Previous assignment restored and verified on both gateways"
					c.Store.event("rollback", current.Tenant, current.Message)
				}
			} else {
				current.Samples += 2
				// Credit only completed probe rounds, never control-plane downtime
				// or a period waiting for gateway convergence.
				current.ObservedSeconds += 2
				if ok {
					current.Consecutive = 0
				} else {
					current.Failures++
					current.Consecutive++
				}
				if current.Consecutive >= 3 {
					t := c.Store.State.tenant(r.Tenant)
					t.Deployment = current.Previous
					current.Status = "reverting"
					current.Message = "Three failed probe rounds; restoring previous assignment"
					c.Store.event("rollback", r.Tenant, current.Message)
				} else if ok && current.ObservedSeconds >= current.ObserveSeconds && current.Samples >= 6 {
					current.Status = "succeeded"
					current.Message = "Observation window passed on both gateways"
					c.Store.event("release", r.Tenant, "Released "+current.Target)
				}
			}
			c.Store.mustSave()
			c.Store.mu.Unlock()
			continue
		}
		c.Store.mu.Lock()
		current := c.findRollout(r.ID)
		if current == nil || current.Status != r.Status {
			c.Store.mu.Unlock()
			continue
		}
		target := c.Store.State.deployment(current.Target)
		if target == nil || target.Retired {
			current.Status = "failed"
			current.Message = "Target deployment unavailable"
		} else if r.Status == "preparing" {
			if target.Status == "ready" && c.XDS.converged() {
				t := c.Store.State.tenant(r.Tenant)
				t.Previous = t.Deployment
				t.Deployment = current.Target
				current.Status = "activating"
				current.Message = "Publishing tenant assignment"
				c.Store.event("release", r.Tenant, "Activating "+current.Target)
			} else if time.Since(current.Created) > 3*time.Minute {
				current.Status = "failed"
				current.Message = "Preparation timed out; previous assignment remains active"
			}
		} else if r.Status == "activating" && c.XDS.converged() {
			now := time.Now().UTC()
			current.Activated = &now
			current.Status = "observing"
			current.Message = fmt.Sprintf("Probing both gateways for %d seconds", current.ObserveSeconds)
			c.Store.event("release", r.Tenant, current.Message)
		}
		c.Store.mustSave()
		c.Store.mu.Unlock()
	}
	// Publish any assignments changed during this tick immediately.
	if err := c.XDS.Publish(ctx, c.Store.copy()); err != nil {
		log.Printf("publish: %v", err)
	}
}
func (c *Controller) findRollout(id string) *Rollout {
	for i := range c.Store.State.Rollouts {
		if c.Store.State.Rollouts[i].ID == id {
			return &c.Store.State.Rollouts[i]
		}
	}
	return nil
}
