package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AwsCli runs one `aws` CLI invocation and returns its standard output. A
// non-zero exit is reported as a *CliExit so the provider can redact it.
type AwsCli interface {
	Run(args []string) ([]byte, error)
}

// CliExit is a CLI process that exited non-zero. It carries only the exit
// code: the process output never reaches a diagnostic or error message.
type CliExit struct {
	Code int
}

func (e *CliExit) Error() string { return "exit " + strconv.Itoa(e.Code) }

// CommandAwsCli shells out to the `aws` executable.
type CommandAwsCli struct{}

// Run executes `aws` with args, discarding standard error.
func (CommandAwsCli) Run(args []string) ([]byte, error) {
	out, err := exec.Command("aws", args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, &CliExit{Code: exit.ExitCode()}
		}
		return nil, fmt.Errorf("run the aws CLI: %w; install it and run `aws configure`", err)
	}
	return out, nil
}

// awsAllowedCommands is the exact set of `<service> <operation>` pairs the
// provider may run. Anything else is refused before the CLI starts.
var awsAllowedCommands = map[string]bool{
	"sts get-caller-identity":             true,
	"route53 list-hosted-zones-by-name":   true,
	"route53 list-resource-record-sets":   true,
	"route53 create-hosted-zone":          true,
	"route53 change-resource-record-sets": true,
}

// AwsProvider implements LiveProvider and DNSProvider against Route 53
// through the local `aws` CLI, which is its only credential source.
type AwsProvider struct {
	cli       AwsCli
	region    string
	profile   string
	account   Account
	zoneIDs   map[string]string
	reference func() string
	hostState *awsHostState
}

var (
	_ LiveProvider = (*AwsProvider)(nil)
	_ DNSProvider  = (*AwsProvider)(nil)
)

func init() {
	Register(ProviderSpec{
		Name:        "aws",
		Kinds:       []string{"dnsRecordSet"},
		ConfigBlock: "aws",
		New: func(_ Settings, block map[string]any) (LiveProvider, error) {
			return NewAwsProviderFromConfig(CommandAwsCli{}, block)
		},
	})
}

// NewAwsProviderFromConfig builds a provider from the attune.yaml aws block,
// which may name region and profile and nothing else.
func NewAwsProviderFromConfig(cli AwsCli, block map[string]any) (*AwsProvider, error) {
	if err := rejectUnknown(block, []string{"region", "profile"}, "attune.yaml aws"); err != nil {
		return nil, err
	}
	region, err := optionalString(block, "region")
	if err != nil {
		return nil, err
	}
	profile, err := optionalString(block, "profile")
	if err != nil {
		return nil, err
	}
	return NewAwsProvider(cli, region, profile), nil
}

// NewAwsProvider builds a provider over cli with an optional region and
// profile, both left to the CLI's own configuration when empty.
func NewAwsProvider(cli AwsCli, region, profile string) *AwsProvider {
	return &AwsProvider{
		cli:       cli,
		region:    region,
		profile:   profile,
		zoneIDs:   map[string]string{},
		reference: func() string { return "attune-" + strconv.FormatInt(time.Now().UnixNano(), 10) },
	}
}

// run executes one allowlisted command with JSON output and the configured
// region and profile, and decodes the result into out when out is non-nil.
func (p *AwsProvider) run(service, operation string, out any, args ...string) error {
	pair := service + " " + operation
	if !awsAllowedCommands[pair] {
		return fmt.Errorf("refuse to run `aws %s`: not an allowlisted command", pair)
	}
	argv := append([]string{service, operation}, args...)
	argv = append(argv, "--output", "json")
	if p.region != "" {
		argv = append(argv, "--region", p.region)
	}
	if p.profile != "" {
		argv = append(argv, "--profile", p.profile)
	}
	raw, err := p.cli.Run(argv)
	if err != nil {
		var exit *CliExit
		if errors.As(err, &exit) {
			return fmt.Errorf("aws %s failed with exit %d", pair, exit.Code)
		}
		return err
	}
	if out == nil || len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("aws %s returned malformed JSON", pair)
	}
	return nil
}

// Ground authenticates through STS and records the account and caller.
func (p *AwsProvider) Ground() (Account, error) {
	var identity struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
	}
	if err := p.run("sts", "get-caller-identity", &identity); err != nil {
		return Account{}, fmt.Errorf("%w; run `aws configure` or `aws sso login` and retry", err)
	}
	p.account = Account{Tenant: identity.Account, Identity: identity.Arn}
	return p.account, nil
}

// Describe renders the non-secret diagnostic fields of the grounded target.
func (p *AwsProvider) Describe() string {
	return fmt.Sprintf("account=%s identity=%s region=%s profile=%s",
		p.account.Tenant, p.account.Identity, orDefault(p.region), orDefault(p.profile))
}

func orDefault(value string) string {
	if value == "" {
		return "default"
	}
	return value
}

type hostedZone struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		PrivateZone bool `json:"PrivateZone"`
	} `json:"Config"`
}

// zoneID returns the hosted zone id for zone, looking it up once. It
// considers public zones only and fails when two share the name.
func (p *AwsProvider) zoneID(zone string) (string, bool, error) {
	if id, ok := p.zoneIDs[zone]; ok {
		return id, true, nil
	}
	var result struct {
		HostedZones []hostedZone `json:"HostedZones"`
	}
	if err := p.run("route53", "list-hosted-zones-by-name", &result, "--dns-name", zone, "--max-items", "10"); err != nil {
		return "", false, err
	}
	var matches []string
	for _, z := range result.HostedZones {
		if z.Name == zone+"." && !z.Config.PrivateZone {
			matches = append(matches, strings.TrimPrefix(z.ID, "/hostedzone/"))
		}
	}
	switch len(matches) {
	case 0:
		return "", false, nil
	case 1:
		p.zoneIDs[zone] = matches[0]
		return matches[0], true, nil
	default:
		return "", false, fmt.Errorf("zone %s: %d public hosted zones share this name; keep one", zone, len(matches))
	}
}

// HasZone reports whether a public hosted zone named zone exists.
func (p *AwsProvider) HasZone(zone string) (bool, error) {
	_, ok, err := p.zoneID(zone)
	return ok, err
}

// EnsureZone creates the hosted zone when it does not exist.
func (p *AwsProvider) EnsureZone(zone string) error {
	if _, ok, err := p.zoneID(zone); err != nil || ok {
		return err
	}
	var result struct {
		HostedZone hostedZone `json:"HostedZone"`
	}
	if err := p.run("route53", "create-hosted-zone", &result, "--name", zone, "--caller-reference", p.reference()); err != nil {
		return err
	}
	p.zoneIDs[zone] = strings.TrimPrefix(result.HostedZone.ID, "/hostedzone/")
	return nil
}

type recordSet struct {
	Name            string `json:"Name"`
	Type            string `json:"Type"`
	TTL             int64  `json:"TTL,omitempty"`
	ResourceRecords []struct {
		Value string `json:"Value"`
	} `json:"ResourceRecords,omitempty"`
	AliasTarget map[string]any `json:"AliasTarget,omitempty"`
}

// ListDNS returns every value-bearing record set in zone in attune's shape:
// the apex as `@`, other names relative to the zone, TXT values unquoted.
// Alias record sets have no values and are left out, so attune neither
// manages nor prunes them.
func (p *AwsProvider) ListDNS(zone string) ([]DnsRecord, error) {
	id, ok, err := p.zoneID(zone)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	var result struct {
		ResourceRecordSets []recordSet `json:"ResourceRecordSets"`
	}
	if err := p.run("route53", "list-resource-record-sets", &result, "--hosted-zone-id", id); err != nil {
		return nil, err
	}
	var records []DnsRecord
	for _, set := range result.ResourceRecordSets {
		if set.AliasTarget != nil {
			continue
		}
		record := DnsRecord{Zone: zone, RecordType: set.Type, Name: relativeName(set.Name, zone), TTL: set.TTL}
		for _, rr := range set.ResourceRecords {
			record.Values = append(record.Values, fromRoute53Value(set.Type, rr.Value))
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Key() < records[j].Key() })
	return records, nil
}

// PutDNS upserts one record set.
func (p *AwsProvider) PutDNS(record DnsRecord) error {
	return p.change("UPSERT", record)
}

// DeleteDNS deletes one record set, sending the live TTL and values that
// Route 53 requires for a DELETE.
func (p *AwsProvider) DeleteDNS(record DnsRecord) error {
	return p.change("DELETE", record)
}

func (p *AwsProvider) change(action string, record DnsRecord) error {
	id, ok, err := p.zoneID(record.Zone)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("zone %s does not exist", record.Zone)
	}
	values := make([]map[string]string, 0, len(record.Values))
	for _, v := range record.Values {
		values = append(values, map[string]string{"Value": toRoute53Value(record.RecordType, v)})
	}
	batch := map[string]any{"Changes": []map[string]any{{
		"Action": action,
		"ResourceRecordSet": map[string]any{
			"Name":            absoluteName(record.Name, record.Zone),
			"Type":            strings.ToUpper(record.RecordType),
			"TTL":             record.TTL,
			"ResourceRecords": values,
		},
	}}}
	encoded, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	return p.run("route53", "change-resource-record-sets", nil, "--hosted-zone-id", id, "--change-batch", string(encoded))
}

// relativeName maps a Route 53 record name to attune's: the zone apex is
// `@`, other names lose the zone suffix and trailing dot, and the octal
// escape Route 53 uses for `*` is restored.
func relativeName(name, zone string) string {
	name = strings.ReplaceAll(name, `\052`, "*")
	name = strings.TrimSuffix(name, ".")
	if name == zone {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone)
}

// absoluteName maps attune's record name to Route 53's fully qualified form.
func absoluteName(name, zone string) string {
	if name == "@" || name == "" {
		return zone + "."
	}
	return name + "." + zone + "."
}

// toRoute53Value quotes a TXT value for Route 53; other types pass through.
func toRoute53Value(recordType, value string) string {
	if strings.ToUpper(recordType) != "TXT" {
		return value
	}
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2 {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

// fromRoute53Value strips the quoting Route 53 keeps on TXT values.
func fromRoute53Value(recordType, value string) string {
	if strings.ToUpper(recordType) != "TXT" {
		return value
	}
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2 {
		return strings.ReplaceAll(value[1:len(value)-1], `\"`, `"`)
	}
	return value
}

// Host kinds. Every resource attune creates carries the tag attune=managed
// (IAM roles and instance profiles also live under the path /attune/), and
// every list reads only those resources, so pruning can never reach a
// resource attune did not create.

const (
	awsManagedTagKey   = "attune"
	awsManagedTagValue = "managed"
	awsIamPath         = "/attune/"
)

// awsEC2TrustPolicy lets the compute service assume an attune role.
const awsEC2TrustPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

func init() {
	for _, pair := range []string{
		"resourcegroupstaggingapi get-resources",
		"iam create-role", "iam delete-role",
		"iam list-attached-role-policies", "iam attach-role-policy", "iam detach-role-policy",
		"iam list-role-policies", "iam get-role-policy", "iam put-role-policy", "iam delete-role-policy",
		"iam list-instance-profiles-for-role", "iam create-instance-profile", "iam delete-instance-profile",
		"iam add-role-to-instance-profile", "iam remove-role-from-instance-profile",
		"s3api create-bucket", "s3api delete-bucket", "s3api put-bucket-tagging",
		"s3api get-bucket-location", "s3api get-public-access-block", "s3api put-public-access-block",
		"s3api get-bucket-encryption", "s3api put-bucket-encryption",
		"ec2 describe-vpcs", "ec2 describe-security-groups", "ec2 create-security-group", "ec2 delete-security-group",
		"ec2 authorize-security-group-ingress", "ec2 revoke-security-group-ingress",
		"ec2 describe-instances", "ec2 run-instances", "ec2 terminate-instances",
		"ec2 describe-volumes", "ec2 modify-volume", "ec2 describe-images", "ec2 create-tags",
		"ec2 modify-instance-metadata-options", "ec2 modify-instance-attribute",
		"ec2 describe-iam-instance-profile-associations", "ec2 associate-iam-instance-profile", "ec2 replace-iam-instance-profile-association",
		"ec2 describe-addresses", "ec2 allocate-address", "ec2 associate-address", "ec2 disassociate-address", "ec2 release-address",
		"ssm get-parameter",
	} {
		awsAllowedCommands[pair] = true
	}
	spec := registry["aws"]
	spec.Kinds = []string{"dnsRecordSet", "iamRole", "bucket", "firewall", "instance", "staticAddress"}
	registry["aws"] = spec
}

var (
	_ IamRoleProvider = (*AwsProvider)(nil)
	_ StorageProvider = (*AwsProvider)(nil)
	_ NetworkProvider = (*AwsProvider)(nil)
	_ ComputeProvider = (*AwsProvider)(nil)
)

// awsHostState caches what one run learns about managed resources: their
// ARNs from the tagging API, and the name-to-id maps apply needs.
type awsHostState struct {
	loaded      bool
	arns        map[string][]string // resource type -> ARNs
	firewallIDs map[string]string   // firewall name -> group id
	instanceIDs map[string]string   // instance name -> instance id
	groupNames  map[string]string   // group id -> firewall name
	instNames   map[string]string   // instance id -> instance name
}

func (p *AwsProvider) host() *awsHostState {
	if p.hostState == nil {
		p.hostState = &awsHostState{arns: map[string][]string{}, firewallIDs: map[string]string{}, instanceIDs: map[string]string{}, groupNames: map[string]string{}, instNames: map[string]string{}}
	}
	return p.hostState
}

// managedARNs returns the ARNs tagged attune=managed for one resource type
// such as "ec2:instance", reading the tagging API once per run.
func (p *AwsProvider) managedARNs(resourceType string) ([]string, error) {
	h := p.host()
	if !h.loaded {
		var result struct {
			List []struct {
				ARN string `json:"ResourceARN"`
			} `json:"ResourceTagMappingList"`
		}
		err := p.run("resourcegroupstaggingapi", "get-resources", &result,
			"--tag-filters", `[{"Key":"`+awsManagedTagKey+`","Values":["`+awsManagedTagValue+`"]}]`,
			"--resource-type-filters", "iam:role", "s3", "ec2:instance", "ec2:security-group", "ec2:elastic-ip")
		if err != nil {
			return nil, err
		}
		for _, item := range result.List {
			h.arns[arnType(item.ARN)] = append(h.arns[arnType(item.ARN)], item.ARN)
		}
		h.loaded = true
	}
	return h.arns[resourceType], nil
}

// arnType classifies an ARN as "iam:role", "s3", or "ec2:<resource>".
func arnType(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return ""
	}
	service, resource := parts[2], parts[5]
	switch service {
	case "s3":
		return "s3"
	case "iam":
		return "iam:" + strings.SplitN(resource, "/", 2)[0]
	case "ec2":
		return "ec2:" + strings.SplitN(resource, "/", 2)[0]
	}
	return service
}

// arnName returns the last path segment of an ARN's resource.
func arnName(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	resource := parts[len(parts)-1]
	return resource[strings.LastIndex(resource, "/")+1:]
}

func awsTagsJSON(name string) string {
	tags := []map[string]string{{"Key": awsManagedTagKey, "Value": awsManagedTagValue}}
	if name != "" {
		tags = append(tags, map[string]string{"Key": "Name", "Value": name})
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func awsTagSpec(resourceType, name string, extra map[string]string) string {
	tags := []map[string]string{{"Key": awsManagedTagKey, "Value": awsManagedTagValue}, {"Key": "Name", "Value": name}}
	for _, k := range sortedKeys(extra) {
		tags = append(tags, map[string]string{"Key": k, "Value": extra[k]})
	}
	b, _ := json.Marshal([]map[string]any{{"ResourceType": resourceType, "Tags": tags}})
	return string(b)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- IAM roles

// ListIamRoles returns every role attune created, with its policies and
// whether it has an instance profile.
func (p *AwsProvider) ListIamRoles() ([]IamRole, error) {
	arns, err := p.managedARNs("iam:role")
	if err != nil {
		return nil, err
	}
	var roles []IamRole
	for _, arn := range arns {
		role, err := p.readIamRole(arnName(arn))
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
	return roles, nil
}

func (p *AwsProvider) readIamRole(name string) (IamRole, error) {
	role := IamRole{Name: name, InlinePolicies: map[string]any{}}
	var attached struct {
		AttachedPolicies []struct {
			PolicyArn string `json:"PolicyArn"`
		} `json:"AttachedPolicies"`
	}
	if err := p.run("iam", "list-attached-role-policies", &attached, "--role-name", name); err != nil {
		return role, err
	}
	for _, a := range attached.AttachedPolicies {
		role.ManagedPolicies = append(role.ManagedPolicies, a.PolicyArn)
	}
	var inline struct {
		PolicyNames []string `json:"PolicyNames"`
	}
	if err := p.run("iam", "list-role-policies", &inline, "--role-name", name); err != nil {
		return role, err
	}
	for _, policyName := range inline.PolicyNames {
		var doc struct {
			PolicyDocument any `json:"PolicyDocument"`
		}
		if err := p.run("iam", "get-role-policy", &doc, "--role-name", name, "--policy-name", policyName); err != nil {
			return role, err
		}
		role.InlinePolicies[policyName] = doc.PolicyDocument
	}
	profiles, err := p.instanceProfiles(name)
	if err != nil {
		return role, err
	}
	role.InstanceProfile = len(profiles) > 0
	return role, nil
}

func (p *AwsProvider) instanceProfiles(role string) ([]string, error) {
	var result struct {
		InstanceProfiles []struct {
			Name string `json:"InstanceProfileName"`
		} `json:"InstanceProfiles"`
	}
	if err := p.run("iam", "list-instance-profiles-for-role", &result, "--role-name", role); err != nil {
		return nil, err
	}
	var names []string
	for _, ip := range result.InstanceProfiles {
		names = append(names, ip.Name)
	}
	return names, nil
}

// PutIamRole creates the role when attune does not own one by that name,
// then reconciles its managed policies, inline policies, and instance
// profile.
func (p *AwsProvider) PutIamRole(role IamRole) error {
	current, exists, err := p.currentIamRole(role.Name)
	if err != nil {
		return err
	}
	if !exists {
		if err := p.run("iam", "create-role", nil, "--role-name", role.Name, "--path", awsIamPath,
			"--assume-role-policy-document", awsEC2TrustPolicy, "--tags", awsTagsJSON("")); err != nil {
			return err
		}
	}
	for _, arn := range role.ManagedPolicies {
		if !slices.Contains(current.ManagedPolicies, arn) {
			if err := p.run("iam", "attach-role-policy", nil, "--role-name", role.Name, "--policy-arn", arn); err != nil {
				return err
			}
		}
	}
	for _, arn := range current.ManagedPolicies {
		if !slices.Contains(role.ManagedPolicies, arn) {
			if err := p.run("iam", "detach-role-policy", nil, "--role-name", role.Name, "--policy-arn", arn); err != nil {
				return err
			}
		}
	}
	for _, name := range sortedAnyKeys(role.InlinePolicies) {
		if before, ok := current.InlinePolicies[name]; !ok || canonicalJSON(before) != canonicalJSON(role.InlinePolicies[name]) {
			if err := p.run("iam", "put-role-policy", nil, "--role-name", role.Name, "--policy-name", name, "--policy-document", canonicalJSON(role.InlinePolicies[name])); err != nil {
				return err
			}
		}
	}
	for _, name := range sortedAnyKeys(current.InlinePolicies) {
		if _, ok := role.InlinePolicies[name]; !ok {
			if err := p.run("iam", "delete-role-policy", nil, "--role-name", role.Name, "--policy-name", name); err != nil {
				return err
			}
		}
	}
	switch {
	case role.InstanceProfile && !current.InstanceProfile:
		if err := p.run("iam", "create-instance-profile", nil, "--instance-profile-name", role.Name, "--path", awsIamPath, "--tags", awsTagsJSON("")); err != nil {
			return err
		}
		return p.run("iam", "add-role-to-instance-profile", nil, "--instance-profile-name", role.Name, "--role-name", role.Name)
	case !role.InstanceProfile && current.InstanceProfile:
		return p.dropInstanceProfiles(role.Name)
	}
	return nil
}

// currentIamRole returns the live role when attune owns one by that name.
func (p *AwsProvider) currentIamRole(name string) (IamRole, bool, error) {
	arns, err := p.managedARNs("iam:role")
	if err != nil {
		return IamRole{}, false, err
	}
	for _, arn := range arns {
		if arnName(arn) == name {
			role, err := p.readIamRole(name)
			return role, true, err
		}
	}
	return IamRole{InlinePolicies: map[string]any{}}, false, nil
}

func (p *AwsProvider) dropInstanceProfiles(role string) error {
	profiles, err := p.instanceProfiles(role)
	if err != nil {
		return err
	}
	for _, profile := range profiles {
		if err := p.run("iam", "remove-role-from-instance-profile", nil, "--instance-profile-name", profile, "--role-name", role); err != nil {
			return err
		}
		if err := p.run("iam", "delete-instance-profile", nil, "--instance-profile-name", profile); err != nil {
			return err
		}
	}
	return nil
}

// DeleteIamRole removes the role's instance profiles and policies, then the
// role.
func (p *AwsProvider) DeleteIamRole(role IamRole) error {
	if err := p.dropInstanceProfiles(role.Name); err != nil {
		return err
	}
	for _, arn := range role.ManagedPolicies {
		if err := p.run("iam", "detach-role-policy", nil, "--role-name", role.Name, "--policy-arn", arn); err != nil {
			return err
		}
	}
	for _, name := range sortedAnyKeys(role.InlinePolicies) {
		if err := p.run("iam", "delete-role-policy", nil, "--role-name", role.Name, "--policy-name", name); err != nil {
			return err
		}
	}
	return p.run("iam", "delete-role", nil, "--role-name", role.Name)
}

func sortedAnyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- Buckets

// ListBuckets returns every bucket attune created, with its region, public
// access block, and default encryption.
func (p *AwsProvider) ListBuckets() ([]Bucket, error) {
	arns, err := p.managedARNs("s3")
	if err != nil {
		return nil, err
	}
	var buckets []Bucket
	for _, arn := range arns {
		bucket, err := p.readBucket(arnName(arn))
		if err != nil {
			return nil, err
		}
		buckets = append(buckets, bucket)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Name < buckets[j].Name })
	return buckets, nil
}

func (p *AwsProvider) readBucket(name string) (Bucket, error) {
	bucket := Bucket{Name: name}
	var location struct {
		LocationConstraint *string `json:"LocationConstraint"`
	}
	if err := p.run("s3api", "get-bucket-location", &location, "--bucket", name); err != nil {
		return bucket, err
	}
	bucket.Region = "us-east-1"
	if location.LocationConstraint != nil && *location.LocationConstraint != "" {
		bucket.Region = *location.LocationConstraint
	}
	var block struct {
		Configuration PublicAccessBlock `json:"PublicAccessBlockConfiguration"`
	}
	if err := p.run("s3api", "get-public-access-block", &block, "--bucket", name); err != nil {
		return bucket, err
	}
	bucket.PublicAccessBlock = block.Configuration
	var encryption struct {
		Configuration struct {
			Rules []struct {
				Default struct {
					Algorithm string `json:"SSEAlgorithm"`
					KeyID     string `json:"KMSMasterKeyID"`
				} `json:"ApplyServerSideEncryptionByDefault"`
			} `json:"Rules"`
		} `json:"ServerSideEncryptionConfiguration"`
	}
	if err := p.run("s3api", "get-bucket-encryption", &encryption, "--bucket", name); err != nil {
		return bucket, err
	}
	if len(encryption.Configuration.Rules) > 0 {
		rule := encryption.Configuration.Rules[0].Default
		if rule.Algorithm == "aws:kms" {
			bucket.Encryption = rule.KeyID
		} else {
			bucket.Encryption = "aes256"
		}
	}
	return bucket, nil
}

// PutBucket creates the bucket when attune does not own one by that name,
// then sets its public access block and default encryption.
func (p *AwsProvider) PutBucket(bucket Bucket) error {
	arns, err := p.managedARNs("s3")
	if err != nil {
		return err
	}
	exists := false
	for _, arn := range arns {
		exists = exists || arnName(arn) == bucket.Name
	}
	if !exists {
		args := []string{"--bucket", bucket.Name, "--region", bucket.Region}
		if bucket.Region != "us-east-1" {
			args = append(args, "--create-bucket-configuration", `{"LocationConstraint":"`+bucket.Region+`"}`)
		}
		if err := p.run("s3api", "create-bucket", nil, args...); err != nil {
			return err
		}
		if err := p.run("s3api", "put-bucket-tagging", nil, "--bucket", bucket.Name, "--tagging", `{"TagSet":`+awsTagsJSON("")+`}`); err != nil {
			return err
		}
	}
	pab, _ := json.Marshal(bucket.PublicAccessBlock)
	if err := p.run("s3api", "put-public-access-block", nil, "--bucket", bucket.Name, "--public-access-block-configuration", string(pab)); err != nil {
		return err
	}
	rule := map[string]any{"SSEAlgorithm": "AES256"}
	if bucket.Encryption != "aes256" {
		rule = map[string]any{"SSEAlgorithm": "aws:kms", "KMSMasterKeyID": bucket.Encryption}
	}
	config, _ := json.Marshal(map[string]any{"Rules": []map[string]any{{"ApplyServerSideEncryptionByDefault": rule}}})
	return p.run("s3api", "put-bucket-encryption", nil, "--bucket", bucket.Name, "--server-side-encryption-configuration", string(config))
}

// DeleteBucket deletes the bucket; a bucket that still holds objects fails.
func (p *AwsProvider) DeleteBucket(bucket Bucket) error {
	return p.run("s3api", "delete-bucket", nil, "--bucket", bucket.Name)
}

// ---- Firewalls (security groups)

type awsSecurityGroup struct {
	GroupID       string `json:"GroupId"`
	GroupName     string `json:"GroupName"`
	VpcID         string `json:"VpcId"`
	IpPermissions []struct {
		IpProtocol string `json:"IpProtocol"`
		FromPort   int64  `json:"FromPort"`
		ToPort     int64  `json:"ToPort"`
		IpRanges   []struct {
			CidrIp string `json:"CidrIp"`
		} `json:"IpRanges"`
		Ipv6Ranges []struct {
			CidrIpv6 string `json:"CidrIpv6"`
		} `json:"Ipv6Ranges"`
		UserIdGroupPairs []struct {
			GroupID string `json:"GroupId"`
		} `json:"UserIdGroupPairs"`
	} `json:"IpPermissions"`
}

// ListFirewalls returns every security group attune created, with its
// rules in attune's shape: group sources named by firewall when attune
// owns them, and the default VPC rendered as an empty vpc.
func (p *AwsProvider) ListFirewalls() ([]Firewall, error) {
	arns, err := p.managedARNs("ec2:security-group")
	if err != nil {
		return nil, err
	}
	if len(arns) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(arns))
	for _, arn := range arns {
		ids = append(ids, arnName(arn))
	}
	var result struct {
		SecurityGroups []awsSecurityGroup `json:"SecurityGroups"`
	}
	if err := p.run("ec2", "describe-security-groups", &result, append([]string{"--group-ids"}, ids...)...); err != nil {
		return nil, err
	}
	h := p.host()
	for _, sg := range result.SecurityGroups {
		h.firewallIDs[sg.GroupName] = sg.GroupID
		h.groupNames[sg.GroupID] = sg.GroupName
	}
	var firewalls []Firewall
	for _, sg := range result.SecurityGroups {
		vpc, err := p.vpcName(sg.VpcID)
		if err != nil {
			return nil, err
		}
		firewalls = append(firewalls, Firewall{ID: sg.GroupID, Name: sg.GroupName, VPC: vpc, Rules: p.rulesFrom(sg)})
	}
	sort.Slice(firewalls, func(i, j int) bool { return firewalls[i].Name < firewalls[j].Name })
	return firewalls, nil
}

func (p *AwsProvider) rulesFrom(sg awsSecurityGroup) []FirewallRule {
	var rules []FirewallRule
	for _, perm := range sg.IpPermissions {
		rule := FirewallRule{Protocol: perm.IpProtocol, FromPort: perm.FromPort, ToPort: perm.ToPort}
		for _, r := range perm.IpRanges {
			rule.Sources = append(rule.Sources, r.CidrIp)
		}
		for _, r := range perm.Ipv6Ranges {
			rule.Sources = append(rule.Sources, r.CidrIpv6)
		}
		for _, g := range perm.UserIdGroupPairs {
			if name, ok := p.host().groupNames[g.GroupID]; ok {
				rule.Sources = append(rule.Sources, name)
			} else {
				rule.Sources = append(rule.Sources, g.GroupID)
			}
		}
		rules = append(rules, rule)
	}
	return rules
}

// vpcName renders a VPC as attune's vpc field: empty for the default VPC,
// its Name tag otherwise, or its id when it has none.
func (p *AwsProvider) vpcName(id string) (string, error) {
	var result struct {
		Vpcs []struct {
			VpcID     string `json:"VpcId"`
			IsDefault bool   `json:"IsDefault"`
			Tags      []struct {
				Key   string `json:"Key"`
				Value string `json:"Value"`
			} `json:"Tags"`
		} `json:"Vpcs"`
	}
	if err := p.run("ec2", "describe-vpcs", &result, "--vpc-ids", id); err != nil {
		return "", err
	}
	if len(result.Vpcs) == 0 {
		return "", fmt.Errorf("vpc %s not found", id)
	}
	vpc := result.Vpcs[0]
	if vpc.IsDefault {
		return "", nil
	}
	for _, t := range vpc.Tags {
		if t.Key == "Name" {
			return t.Value, nil
		}
	}
	return vpc.VpcID, nil
}

// vpcID resolves attune's vpc field to a VPC id: the default VPC when
// empty, otherwise the VPC with that Name tag.
func (p *AwsProvider) vpcID(name string) (string, error) {
	filter := `[{"Name":"is-default","Values":["true"]}]`
	if name != "" {
		filter = `[{"Name":"tag:Name","Values":["` + name + `"]}]`
	}
	var result struct {
		Vpcs []struct {
			VpcID string `json:"VpcId"`
		} `json:"Vpcs"`
	}
	if err := p.run("ec2", "describe-vpcs", &result, "--filters", filter); err != nil {
		return "", err
	}
	if len(result.Vpcs) != 1 {
		return "", fmt.Errorf("vpc %q: expected one match, found %d", orDefault(name), len(result.Vpcs))
	}
	return result.Vpcs[0].VpcID, nil
}

// permissionsJSON renders rules as IpPermissions, resolving firewall names
// to group ids.
func (p *AwsProvider) permissionsJSON(rules []FirewallRule) string {
	var perms []map[string]any
	for _, rule := range rules {
		perm := map[string]any{"IpProtocol": rule.Protocol, "FromPort": rule.FromPort, "ToPort": rule.ToPort}
		var v4, v6, groups []map[string]string
		for _, source := range rule.Sources {
			switch {
			case strings.HasPrefix(source, "sg-"):
				groups = append(groups, map[string]string{"GroupId": source})
			case strings.Contains(source, ":"):
				v6 = append(v6, map[string]string{"CidrIpv6": source})
			case strings.Contains(source, "/"):
				v4 = append(v4, map[string]string{"CidrIp": source})
			default:
				if id, ok := p.host().firewallIDs[source]; ok {
					groups = append(groups, map[string]string{"GroupId": id})
				} else {
					groups = append(groups, map[string]string{"GroupId": source})
				}
			}
		}
		if len(v4) > 0 {
			perm["IpRanges"] = v4
		}
		if len(v6) > 0 {
			perm["Ipv6Ranges"] = v6
		}
		if len(groups) > 0 {
			perm["UserIdGroupPairs"] = groups
		}
		perms = append(perms, perm)
	}
	b, _ := json.Marshal(perms)
	return string(b)
}

// PutFirewall creates the group when the firewall has no live id, then
// authorizes the rules the live group lacks and revokes the ones the spec
// dropped.
func (p *AwsProvider) PutFirewall(firewall Firewall) error {
	h := p.host()
	var current []FirewallRule
	if firewall.ID == "" {
		vpc, err := p.vpcID(firewall.VPC)
		if err != nil {
			return err
		}
		var created struct {
			GroupID string `json:"GroupId"`
		}
		if err := p.run("ec2", "create-security-group", &created, "--group-name", firewall.Name, "--description", "managed by attune",
			"--vpc-id", vpc, "--tag-specifications", awsTagSpec("security-group", firewall.Name, nil)); err != nil {
			return err
		}
		firewall.ID = created.GroupID
		h.firewallIDs[firewall.Name] = created.GroupID
		h.groupNames[created.GroupID] = firewall.Name
	} else {
		var result struct {
			SecurityGroups []awsSecurityGroup `json:"SecurityGroups"`
		}
		if err := p.run("ec2", "describe-security-groups", &result, "--group-ids", firewall.ID); err != nil {
			return err
		}
		if len(result.SecurityGroups) == 1 {
			current = p.rulesFrom(result.SecurityGroups[0])
		}
	}
	add, remove := atomicRuleDiff(current, firewall.Rules)
	if len(remove) > 0 {
		if err := p.run("ec2", "revoke-security-group-ingress", nil, "--group-id", firewall.ID, "--ip-permissions", p.permissionsJSON(remove)); err != nil {
			return err
		}
	}
	if len(add) > 0 {
		return p.run("ec2", "authorize-security-group-ingress", nil, "--group-id", firewall.ID, "--ip-permissions", p.permissionsJSON(add))
	}
	return nil
}

// atomicRuleDiff expands both rule sets to one source per rule and returns
// the rules to add and to remove.
func atomicRuleDiff(current, desired []FirewallRule) (add, remove []FirewallRule) {
	have := map[string]FirewallRule{}
	for _, r := range atomicRules(current) {
		have[r.key()] = r
	}
	want := map[string]FirewallRule{}
	for _, r := range atomicRules(desired) {
		want[r.key()] = r
	}
	for _, k := range sortedRuleKeys(want) {
		if _, ok := have[k]; !ok {
			add = append(add, want[k])
		}
	}
	for _, k := range sortedRuleKeys(have) {
		if _, ok := want[k]; !ok {
			remove = append(remove, have[k])
		}
	}
	return add, remove
}

func sortedRuleKeys(m map[string]FirewallRule) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// DeleteFirewall deletes the security group.
func (p *AwsProvider) DeleteFirewall(firewall Firewall) error {
	return p.run("ec2", "delete-security-group", nil, "--group-id", firewall.ID)
}

// ---- Instances

type awsInstance struct {
	InstanceID   string `json:"InstanceId"`
	InstanceType string `json:"InstanceType"`
	ImageID      string `json:"ImageId"`
	State        struct {
		Name string `json:"Name"`
	} `json:"State"`
	IamInstanceProfile *struct {
		Arn string `json:"Arn"`
	} `json:"IamInstanceProfile"`
	SecurityGroups []struct {
		GroupID string `json:"GroupId"`
	} `json:"SecurityGroups"`
	MetadataOptions struct {
		HttpTokens string `json:"HttpTokens"`
	} `json:"MetadataOptions"`
	RootDeviceName      string `json:"RootDeviceName"`
	BlockDeviceMappings []struct {
		DeviceName string `json:"DeviceName"`
		Ebs        struct {
			VolumeID string `json:"VolumeId"`
		} `json:"Ebs"`
	} `json:"BlockDeviceMappings"`
	Tags []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"Tags"`
}

func (i awsInstance) tag(key string) string {
	for _, t := range i.Tags {
		if t.Key == key {
			return t.Value
		}
	}
	return ""
}

func (i awsInstance) rootVolumeID() string {
	for _, m := range i.BlockDeviceMappings {
		if m.DeviceName == i.RootDeviceName {
			return m.Ebs.VolumeID
		}
	}
	return ""
}

// ResolveImage returns an image id unchanged and resolves a parameter path
// through SSM.
func (p *AwsProvider) ResolveImage(image string) (string, error) {
	if strings.HasPrefix(image, "ami-") {
		return image, nil
	}
	var result struct {
		Parameter struct {
			Value string `json:"Value"`
		} `json:"Parameter"`
	}
	if err := p.run("ssm", "get-parameter", &result, "--name", image); err != nil {
		return "", err
	}
	if result.Parameter.Value == "" {
		return "", fmt.Errorf("image parameter %s has no value", image)
	}
	return result.Parameter.Value, nil
}

// ListInstances returns every running or stopped instance attune created.
func (p *AwsProvider) ListInstances() ([]Instance, error) {
	arns, err := p.managedARNs("ec2:instance")
	if err != nil {
		return nil, err
	}
	if len(arns) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(arns))
	for _, arn := range arns {
		ids = append(ids, arnName(arn))
	}
	raw, err := p.describeInstances(ids)
	if err != nil {
		return nil, err
	}
	var instances []Instance
	for _, inst := range raw {
		if inst.State.Name == "terminated" || inst.State.Name == "shutting-down" {
			continue
		}
		instance, err := p.instanceFrom(inst)
		if err != nil {
			return nil, err
		}
		instances = append(instances, instance)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })
	return instances, nil
}

func (p *AwsProvider) describeInstances(ids []string) ([]awsInstance, error) {
	var result struct {
		Reservations []struct {
			Instances []awsInstance `json:"Instances"`
		} `json:"Reservations"`
	}
	if err := p.run("ec2", "describe-instances", &result, append([]string{"--instance-ids"}, ids...)...); err != nil {
		return nil, err
	}
	var all []awsInstance
	for _, r := range result.Reservations {
		all = append(all, r.Instances...)
	}
	return all, nil
}

func (p *AwsProvider) instanceFrom(inst awsInstance) (Instance, error) {
	h := p.host()
	instance := Instance{ID: inst.InstanceID, Name: inst.tag("Name"), Type: inst.InstanceType, Image: inst.ImageID,
		Imdsv2Required: inst.MetadataOptions.HttpTokens == "required", Tags: map[string]string{}}
	h.instanceIDs[instance.Name] = inst.InstanceID
	h.instNames[inst.InstanceID] = instance.Name
	if inst.IamInstanceProfile != nil {
		instance.Role = arnName(inst.IamInstanceProfile.Arn)
	}
	if len(inst.SecurityGroups) > 0 {
		id := inst.SecurityGroups[0].GroupID
		if name, ok := h.groupNames[id]; ok {
			instance.Firewall = name
		} else {
			instance.Firewall = id
		}
	}
	for _, t := range inst.Tags {
		if t.Key != "Name" && t.Key != awsManagedTagKey {
			instance.Tags[t.Key] = t.Value
		}
	}
	if volume := inst.rootVolumeID(); volume != "" {
		var result struct {
			Volumes []struct {
				Size       int64  `json:"Size"`
				VolumeType string `json:"VolumeType"`
				Encrypted  bool   `json:"Encrypted"`
			} `json:"Volumes"`
		}
		if err := p.run("ec2", "describe-volumes", &result, "--volume-ids", volume); err != nil {
			return instance, err
		}
		if len(result.Volumes) == 1 {
			v := result.Volumes[0]
			instance.RootDisk = RootDisk{Size: v.Size, Type: v.VolumeType, Encrypted: v.Encrypted}
		}
	}
	return instance, nil
}

func (p *AwsProvider) firewallID(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if strings.HasPrefix(name, "sg-") {
		return name, nil
	}
	if id, ok := p.host().firewallIDs[name]; ok {
		return id, nil
	}
	if _, err := p.ListFirewalls(); err != nil {
		return "", err
	}
	if id, ok := p.host().firewallIDs[name]; ok {
		return id, nil
	}
	return "", fmt.Errorf("firewall %q is not one attune manages", name)
}

func (p *AwsProvider) instanceID(name string) (string, error) {
	if id, ok := p.host().instanceIDs[name]; ok {
		return id, nil
	}
	if _, err := p.ListInstances(); err != nil {
		return "", err
	}
	if id, ok := p.host().instanceIDs[name]; ok {
		return id, nil
	}
	return "", fmt.Errorf("instance %q is not one attune manages", name)
}

func metadataOptionsJSON(imdsv2 bool) string {
	tokens := "optional"
	if imdsv2 {
		tokens = "required"
	}
	return `{"HttpTokens":"` + tokens + `","HttpEndpoint":"enabled"}`
}

// PutInstance launches the instance when it has no live id, with its image
// already resolved, and otherwise brings its mutable settings to the spec:
// tags, metadata options, firewall, role, and root disk size.
func (p *AwsProvider) PutInstance(instance Instance) error {
	groupID, err := p.firewallID(instance.Firewall)
	if err != nil {
		return err
	}
	if instance.ID == "" {
		var images struct {
			Images []struct {
				RootDeviceName string `json:"RootDeviceName"`
			} `json:"Images"`
		}
		if err := p.run("ec2", "describe-images", &images, "--image-ids", instance.Image); err != nil {
			return err
		}
		if len(images.Images) != 1 {
			return fmt.Errorf("image %s not found", instance.Image)
		}
		mapping, _ := json.Marshal([]map[string]any{{"DeviceName": images.Images[0].RootDeviceName, "Ebs": map[string]any{
			"VolumeSize": instance.RootDisk.Size, "VolumeType": instance.RootDisk.Type, "Encrypted": instance.RootDisk.Encrypted, "DeleteOnTermination": true}}})
		args := []string{"--image-id", instance.Image, "--instance-type", instance.Type, "--count", "1",
			"--metadata-options", metadataOptionsJSON(instance.Imdsv2Required),
			"--block-device-mappings", string(mapping),
			"--tag-specifications", awsTagSpec("instance", instance.Name, instance.Tags)}
		if instance.Role != "" {
			args = append(args, "--iam-instance-profile", `{"Name":"`+instance.Role+`"}`)
		}
		if groupID != "" {
			args = append(args, "--security-group-ids", groupID)
		}
		var launched struct {
			Instances []awsInstance `json:"Instances"`
		}
		if err := p.run("ec2", "run-instances", &launched, args...); err != nil {
			return err
		}
		if len(launched.Instances) == 1 {
			p.host().instanceIDs[instance.Name] = launched.Instances[0].InstanceID
			p.host().instNames[launched.Instances[0].InstanceID] = instance.Name
		}
		return nil
	}

	raw, err := p.describeInstances([]string{instance.ID})
	if err != nil {
		return err
	}
	if len(raw) != 1 {
		return fmt.Errorf("instance %s not found", instance.ID)
	}
	current, err := p.instanceFrom(raw[0])
	if err != nil {
		return err
	}
	if !tagsSatisfy(instance.Tags, current.Tags) {
		tags := []map[string]string{}
		for _, k := range sortedKeys(instance.Tags) {
			tags = append(tags, map[string]string{"Key": k, "Value": instance.Tags[k]})
		}
		b, _ := json.Marshal(tags)
		if err := p.run("ec2", "create-tags", nil, "--resources", instance.ID, "--tags", string(b)); err != nil {
			return err
		}
	}
	if current.Imdsv2Required != instance.Imdsv2Required {
		tokens := "optional"
		if instance.Imdsv2Required {
			tokens = "required"
		}
		if err := p.run("ec2", "modify-instance-metadata-options", nil, "--instance-id", instance.ID, "--http-tokens", tokens, "--http-endpoint", "enabled"); err != nil {
			return err
		}
	}
	if groupID != "" && current.Firewall != instance.Firewall {
		if err := p.run("ec2", "modify-instance-attribute", nil, "--instance-id", instance.ID, "--groups", groupID); err != nil {
			return err
		}
	}
	if instance.Role != "" && current.Role != instance.Role {
		var associations struct {
			List []struct {
				AssociationID string `json:"AssociationId"`
			} `json:"IamInstanceProfileAssociations"`
		}
		if err := p.run("ec2", "describe-iam-instance-profile-associations", &associations, "--filters", `[{"Name":"instance-id","Values":["`+instance.ID+`"]}]`); err != nil {
			return err
		}
		profile := `{"Name":"` + instance.Role + `"}`
		if len(associations.List) > 0 {
			if err := p.run("ec2", "replace-iam-instance-profile-association", nil, "--association-id", associations.List[0].AssociationID, "--iam-instance-profile", profile); err != nil {
				return err
			}
		} else if err := p.run("ec2", "associate-iam-instance-profile", nil, "--instance-id", instance.ID, "--iam-instance-profile", profile); err != nil {
			return err
		}
	}
	if volume := raw[0].rootVolumeID(); volume != "" && current.RootDisk.Size != instance.RootDisk.Size {
		if err := p.run("ec2", "modify-volume", nil, "--volume-id", volume, "--size", strconv.FormatInt(instance.RootDisk.Size, 10)); err != nil {
			return err
		}
	}
	return nil
}

// DeleteInstance terminates the instance.
func (p *AwsProvider) DeleteInstance(instance Instance) error {
	return p.run("ec2", "terminate-instances", nil, "--instance-ids", instance.ID)
}

// ---- Static addresses

type awsAddress struct {
	AllocationID  string `json:"AllocationId"`
	PublicIP      string `json:"PublicIp"`
	InstanceID    string `json:"InstanceId"`
	AssociationID string `json:"AssociationId"`
	Tags          []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"Tags"`
}

func (p *AwsProvider) describeAddresses(ids []string) ([]awsAddress, error) {
	var result struct {
		Addresses []awsAddress `json:"Addresses"`
	}
	if err := p.run("ec2", "describe-addresses", &result, append([]string{"--allocation-ids"}, ids...)...); err != nil {
		return nil, err
	}
	return result.Addresses, nil
}

// ListAddresses returns every address attune allocated, attached instances
// named when attune manages them.
func (p *AwsProvider) ListAddresses() ([]StaticAddress, error) {
	arns, err := p.managedARNs("ec2:elastic-ip")
	if err != nil {
		return nil, err
	}
	if len(arns) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(arns))
	for _, arn := range arns {
		ids = append(ids, arnName(arn))
	}
	raw, err := p.describeAddresses(ids)
	if err != nil {
		return nil, err
	}
	if len(p.host().instNames) == 0 {
		if _, err := p.ListInstances(); err != nil {
			return nil, err
		}
	}
	var addresses []StaticAddress
	for _, a := range raw {
		address := StaticAddress{ID: a.AllocationID, PublicIP: a.PublicIP}
		for _, t := range a.Tags {
			if t.Key == "Name" {
				address.Name = t.Value
			}
		}
		if a.InstanceID != "" {
			if name, ok := p.host().instNames[a.InstanceID]; ok {
				address.Instance = name
			} else {
				address.Instance = a.InstanceID
			}
		}
		addresses = append(addresses, address)
	}
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].Name < addresses[j].Name })
	return addresses, nil
}

// PutAddress allocates the address when it has no live id, then attaches it
// to its instance, moving it when it is attached elsewhere. It returns the
// public address.
func (p *AwsProvider) PutAddress(address StaticAddress) (string, error) {
	instanceID, err := p.instanceID(address.Instance)
	if err != nil {
		return "", err
	}
	if address.ID == "" {
		var allocated awsAddress
		if err := p.run("ec2", "allocate-address", &allocated, "--domain", "vpc", "--tag-specifications", awsTagSpec("elastic-ip", address.Name, nil)); err != nil {
			return "", err
		}
		if err := p.run("ec2", "associate-address", nil, "--allocation-id", allocated.AllocationID, "--instance-id", instanceID); err != nil {
			return "", err
		}
		return allocated.PublicIP, nil
	}
	live, err := p.describeAddresses([]string{address.ID})
	if err != nil {
		return "", err
	}
	if len(live) != 1 {
		return "", fmt.Errorf("address %s not found", address.ID)
	}
	if live[0].AssociationID != "" {
		if err := p.run("ec2", "disassociate-address", nil, "--association-id", live[0].AssociationID); err != nil {
			return "", err
		}
	}
	if err := p.run("ec2", "associate-address", nil, "--allocation-id", address.ID, "--instance-id", instanceID); err != nil {
		return "", err
	}
	return live[0].PublicIP, nil
}

// DeleteAddress detaches the address when attached, then releases it.
func (p *AwsProvider) DeleteAddress(address StaticAddress) error {
	live, err := p.describeAddresses([]string{address.ID})
	if err != nil {
		return err
	}
	if len(live) == 1 && live[0].AssociationID != "" {
		if err := p.run("ec2", "disassociate-address", nil, "--association-id", live[0].AssociationID); err != nil {
			return err
		}
	}
	return p.run("ec2", "release-address", nil, "--allocation-id", address.ID)
}
