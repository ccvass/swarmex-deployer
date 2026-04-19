package deployer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
)

const (
	labelStrategy      = "swarmex.deployer.strategy"
	labelShiftInterval = "swarmex.deployer.shift-interval"
	labelShiftStep     = "swarmex.deployer.shift-step"
	labelErrorThresh   = "swarmex.deployer.error-threshold"
	labelRollback      = "swarmex.deployer.rollback-on-fail"

	// Internal label to mark green services
	labelGreenOf = "swarmex.deployer.green-of"
)

// DeployConfig parsed from service labels.
type DeployConfig struct {
	Strategy      string        // "blue-green", "canary", or "rolling" (default)
	ShiftInterval time.Duration // time between weight shifts
	ShiftStep     int           // percentage to shift per step
	ErrorThresh   float64       // error % to trigger rollback
	Rollback      bool
}

// Deployment tracks an active blue/green deployment.
type Deployment struct {
	BlueID  string
	GreenID string
	Config  DeployConfig
	Weight  int // current green weight (0-100)
}

// Deployer manages blue/green deployments via Traefik weighted services.
type Deployer struct {
	docker        *client.Client
	prometheusURL string
	logger        *slog.Logger
	active        map[string]*Deployment // keyed by blue service ID
	mu            sync.Mutex
	httpClient    *http.Client
}

// New creates a Deployer.
func New(cli *client.Client, prometheusURL string, logger *slog.Logger) *Deployer {
	return &Deployer{
		docker:        cli,
		prometheusURL: prometheusURL,
		logger:        logger,
		active:        make(map[string]*Deployment),
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
}

// StartDeployment initiates a blue/green deployment for a service with a new image.
func (d *Deployer) StartDeployment(ctx context.Context, serviceID, newImage string) error {
	blue, _, err := d.docker.ServiceInspectWithRaw(ctx, serviceID, types.ServiceInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect blue service: %w", err)
	}

	cfg := parseDeployConfig(blue.Spec.Labels)
	if cfg.Strategy != "blue-green" && cfg.Strategy != "canary" {
		return fmt.Errorf("service %s strategy is %q, expected blue-green or canary", blue.Spec.Name, cfg.Strategy)
	}

	// Create green service as a copy with new image
	greenSpec := blue.Spec
	greenSpec.Name = blue.Spec.Name + "-green"
	greenSpec.TaskTemplate.ContainerSpec.Image = newImage

	// Canary: start with 1 replica regardless of blue replica count
	if cfg.Strategy == "canary" {
		one := uint64(1)
		greenSpec.Mode = swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &one}}
	}

	if greenSpec.Labels == nil {
		greenSpec.Labels = make(map[string]string)
	}
	greenSpec.Labels[labelGreenOf] = serviceID
	// Start with 0 weight — Traefik won't route to it yet
	greenSpec.Labels["traefik.http.services."+greenSpec.Name+".loadbalancer.server.weight"] = "0"

	greenSvc, err := d.docker.ServiceCreate(ctx, greenSpec, types.ServiceCreateOptions{})
	if err != nil {
		return fmt.Errorf("create green service: %w", err)
	}

	dep := &Deployment{
		BlueID:  serviceID,
		GreenID: greenSvc.ID,
		Config:  cfg,
		Weight:  0,
	}

	d.mu.Lock()
	d.active[serviceID] = dep
	d.mu.Unlock()

	d.logger.Info("blue/green deployment started",
		"blue", blue.Spec.Name, "green", greenSpec.Name, "image", newImage)

	go d.shiftLoop(context.Background(), dep, blue.Spec.Name)
	return nil
}

func (d *Deployer) shiftLoop(ctx context.Context, dep *Deployment, blueName string) {
	ticker := time.NewTicker(dep.Config.ShiftInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dep.Weight += dep.Config.ShiftStep
			if dep.Weight > 100 {
				dep.Weight = 100
			}

			// Check error rate before shifting
			errRate := d.queryErrorRate(ctx, blueName+"-green")
			if errRate > dep.Config.ErrorThresh && dep.Config.Rollback {
				d.logger.Error("error threshold exceeded, rolling back",
					"service", blueName, "error_rate", errRate, "threshold", dep.Config.ErrorThresh)
				d.rollback(ctx, dep)
				return
			}

			d.logger.Info("shifting traffic",
				"service", blueName, "green_weight", dep.Weight, "error_rate", fmt.Sprintf("%.1f%%", errRate))

			d.updateWeight(ctx, dep.GreenID, dep.Weight)
			d.updateWeight(ctx, dep.BlueID, 100-dep.Weight)

			if dep.Weight >= 100 {
				d.logger.Info("deployment complete, cleaning up blue", "service", blueName)
				d.finalize(ctx, dep)
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

func (d *Deployer) updateWeight(ctx context.Context, serviceID string, weight int) {
	svc, _, err := d.docker.ServiceInspectWithRaw(ctx, serviceID, types.ServiceInspectOptions{})
	if err != nil {
		return
	}
	if svc.Spec.Labels == nil {
		svc.Spec.Labels = make(map[string]string)
	}
	svc.Spec.Labels["traefik.http.services."+svc.Spec.Name+".loadbalancer.server.weight"] = strconv.Itoa(weight)
	d.docker.ServiceUpdate(ctx, serviceID, svc.Version, svc.Spec, types.ServiceUpdateOptions{})
}

func (d *Deployer) rollback(ctx context.Context, dep *Deployment) {
	// Remove green, restore blue to full weight
	d.updateWeight(ctx, dep.BlueID, 100)
	d.docker.ServiceRemove(ctx, dep.GreenID)

	d.mu.Lock()
	delete(d.active, dep.BlueID)
	d.mu.Unlock()
}

func (d *Deployer) finalize(ctx context.Context, dep *Deployment) {
	// Green becomes the new service — update blue image to green's image, remove green
	green, _, err := d.docker.ServiceInspectWithRaw(ctx, dep.GreenID, types.ServiceInspectOptions{})
	if err != nil {
		return
	}
	blue, _, err := d.docker.ServiceInspectWithRaw(ctx, dep.BlueID, types.ServiceInspectOptions{})
	if err != nil {
		return
	}

	blue.Spec.TaskTemplate.ContainerSpec.Image = green.Spec.TaskTemplate.ContainerSpec.Image
	d.updateWeight(ctx, dep.BlueID, 100)
	d.docker.ServiceUpdate(ctx, dep.BlueID, blue.Version, blue.Spec, types.ServiceUpdateOptions{})
	d.docker.ServiceRemove(ctx, dep.GreenID)

	d.mu.Lock()
	delete(d.active, dep.BlueID)
	d.mu.Unlock()
}

func (d *Deployer) queryErrorRate(ctx context.Context, serviceName string) float64 {
	query := fmt.Sprintf(
		`sum(rate(traefik_service_requests_total{service="%s",code=~"5.."}[1m])) / sum(rate(traefik_service_requests_total{service="%s"}[1m])) * 100`,
		serviceName, serviceName)

	u := fmt.Sprintf("%s/api/v1/query?query=%s", d.prometheusURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Data.Result) == 0 {
		return 0
	}
	if len(result.Data.Result[0].Value) < 2 {
		return 0
	}
	var valStr string
	if err := json.Unmarshal(result.Data.Result[0].Value[1], &valStr); err != nil {
		return 0
	}
	v, _ := strconv.ParseFloat(valStr, 64)
	return v
}

// ActiveDeployments returns the number of active deployments.
func (d *Deployer) ActiveDeployments() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.active)
}

func parseDeployConfig(labels map[string]string) DeployConfig {
	cfg := DeployConfig{
		Strategy:      "rolling",
		ShiftInterval: 30 * time.Second,
		ShiftStep:     20,
		ErrorThresh:   5,
		Rollback:      true,
	}
	if v, ok := labels[labelStrategy]; ok {
		cfg.Strategy = v
	}
	// Canary defaults: conservative (5% steps, 60s interval, 5% error threshold)
	if cfg.Strategy == "canary" {
		cfg.ShiftStep = 5
		cfg.ShiftInterval = 60 * time.Second
		cfg.ErrorThresh = 5
	}
	if d, err := time.ParseDuration(labels[labelShiftInterval]); err == nil {
		cfg.ShiftInterval = d
	}
	if n, err := strconv.Atoi(labels[labelShiftStep]); err == nil {
		cfg.ShiftStep = n
	}
	if f, err := strconv.ParseFloat(labels[labelErrorThresh], 64); err == nil {
		cfg.ErrorThresh = f
	}
	if labels[labelRollback] == "false" {
		cfg.Rollback = false
	}
	return cfg
}

