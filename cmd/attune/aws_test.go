package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/queone/gkit/color"
)

// fakeAwsCli replays recorded output per `<service> <operation>` pair, in
// order, repeating the last recording when a pair runs more often than it
// was recorded, and keeps every argv for assertions.
type fakeAwsCli struct {
	recorded map[string][]fakeAwsResult
	calls    [][]string
}

type fakeAwsResult struct {
	out  string
	exit int
}

func (f *fakeAwsCli) Run(args []string) ([]byte, error) {
	f.calls = append(f.calls, args)
	pair := args[0] + " " + args[1]
	results := f.recorded[pair]
	if len(results) == 0 {
		return nil, &CliExit{Code: 255}
	}
	result := results[0]
	if len(results) > 1 {
		f.recorded[pair] = results[1:]
	}
	if result.exit != 0 {
		return nil, &CliExit{Code: result.exit}
	}
	return []byte(result.out), nil
}

func (f *fakeAwsCli) pairs() []string {
	var pairs []string
	for _, c := range f.calls {
		pairs = append(pairs, c[0]+" "+c[1])
	}
	return pairs
}

func (f *fakeAwsCli) argAfter(call []string, flag string) string {
	for i, a := range call {
		if a == flag && i+1 < len(call) {
			return call[i+1]
		}
	}
	return ""
}

const (
	awsIdentityJSON  = `{"UserId":"AIDAEXAMPLE","Account":"111111111111","Arn":"arn:aws:iam::111111111111:user/example"}`
	awsZoneListJSON  = `{"HostedZones":[{"Id":"/hostedzone/Z0EXAMPLE","Name":"example.com.","Config":{"PrivateZone":false}}]}`
	awsNoZoneJSON    = `{"HostedZones":[]}`
	awsCreateZone    = `{"HostedZone":{"Id":"/hostedzone/Z0CREATED","Name":"example.com."}}`
	awsRecordSetJSON = `{"ResourceRecordSets":[
		{"Name":"example.com.","Type":"NS","TTL":172800,"ResourceRecords":[{"Value":"ns-1.awsdns-00.com."}]},
		{"Name":"example.com.","Type":"SOA","TTL":900,"ResourceRecords":[{"Value":"ns-1.awsdns-00.com. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400"}]},
		{"Name":"example.com.","Type":"TXT","TTL":300,"ResourceRecords":[{"Value":"\"v=spf1 -all\""}]},
		{"Name":"docs.example.com.","Type":"A","TTL":300,"ResourceRecords":[{"Value":"192.0.2.10"}]},
		{"Name":"\\052.example.com.","Type":"A","TTL":300,"ResourceRecords":[{"Value":"192.0.2.11"}]},
		{"Name":"alias.example.com.","Type":"A","AliasTarget":{"HostedZoneId":"Z2FDTNDATAQYW2","DNSName":"d.cloudfront.net.","EvaluateTargetHealth":false}}
	]}`
)

func newAwsFake(recorded map[string][]fakeAwsResult) (*AwsProvider, *fakeAwsCli) {
	cli := &fakeAwsCli{recorded: recorded}
	provider := NewAwsProvider(cli, "us-east-1", "example")
	provider.reference = func() string { return "attune-test" }
	return provider, cli
}

func exampleDNSBundle() *Bundle {
	return &Bundle{Dns: []DnsRecord{
		{Provider: "aws", Zone: "example.com", RecordType: "A", Name: "docs", TTL: 300, Values: []string{"192.0.2.10"}},
		{Provider: "aws", Zone: "example.com", RecordType: "TXT", Name: "@", TTL: 300, Values: []string{"v=spf1 -all"}},
	}}
}

// TestAwsGroundingAndDescribe — AT5: Ground reads the account and ARN
// through STS, Describe renders them with the configured or default region
// and profile, and the config block rejects unknown keys by name.
func TestAwsGroundingAndDescribe(t *testing.T) {
	provider, cli := newAwsFake(map[string][]fakeAwsResult{"sts get-caller-identity": {{out: awsIdentityJSON}}})
	account, err := provider.Ground()
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if account.Tenant != "111111111111" || account.Identity != "arn:aws:iam::111111111111:user/example" {
		t.Errorf("account = %+v", account)
	}
	if got, want := provider.Describe(), "account=111111111111 identity=arn:aws:iam::111111111111:user/example region=us-east-1 profile=example"; got != want {
		t.Errorf("Describe = %q, want %q", got, want)
	}
	call := cli.calls[0]
	if cli.argAfter(call, "--region") != "us-east-1" || cli.argAfter(call, "--profile") != "example" || cli.argAfter(call, "--output") != "json" {
		t.Errorf("argv = %v, want region, profile, and json output", call)
	}
	plain, err := NewAwsProviderFromConfig(&fakeAwsCli{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.Describe(); !strings.HasSuffix(got, "region=default profile=default") {
		t.Errorf("Describe without a block = %q", got)
	}
	_, err = NewAwsProviderFromConfig(&fakeAwsCli{}, map[string]any{"region": "us-east-1", "token": "x"})
	if err == nil || !strings.Contains(err.Error(), `"token"`) {
		t.Errorf("unknown block key: %v", err)
	}
	spec, err := LookupProvider("aws")
	if err != nil || spec.ConfigBlock != "aws" || spec.Kinds[0] != "dnsRecordSet" {
		t.Errorf("aws registry entry = %+v, %v", spec, err)
	}
}

// TestAwsCreatesZoneThenUpsertsRecords — AT2: with no hosted zone, Plan
// creates the zone ahead of the records and Apply drives Route 53 in that
// order with the apex and TXT value mapped.
func TestAwsCreatesZoneThenUpsertsRecords(t *testing.T) {
	provider, cli := newAwsFake(map[string][]fakeAwsResult{
		"route53 list-hosted-zones-by-name":   {{out: awsNoZoneJSON}},
		"route53 create-hosted-zone":          {{out: awsCreateZone}},
		"route53 change-resource-record-sets": {{out: `{"ChangeInfo":{"Status":"PENDING"}}`}},
	})
	changes, err := Plan(provider, exampleDNSBundle(), &Options{Provider: "aws", PruneDNS: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var keys []string
	for _, c := range changes {
		keys = append(keys, c.Action.String()+" "+c.Kind+" "+c.Key)
	}
	want := []string{"create dnsZone dnsZone|example.com", "create dnsRecordSet example.com|A|docs", "create dnsRecordSet example.com|TXT|@"}
	if strings.Join(keys, ";") != strings.Join(want, ";") {
		t.Fatalf("changes = %v, want %v", keys, want)
	}
	if err := Apply(provider, changes, "", nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	pairs := cli.pairs()
	// A missing zone is never cached, so Apply looks it up again before creating it.
	wantPairs := []string{"route53 list-hosted-zones-by-name", "route53 list-hosted-zones-by-name", "route53 create-hosted-zone", "route53 change-resource-record-sets", "route53 change-resource-record-sets"}
	if strings.Join(pairs, ";") != strings.Join(wantPairs, ";") {
		t.Fatalf("calls = %v, want %v", pairs, wantPairs)
	}
	create := cli.calls[2]
	if cli.argAfter(create, "--name") != "example.com" || cli.argAfter(create, "--caller-reference") != "attune-test" {
		t.Errorf("create-hosted-zone argv = %v", create)
	}
	for i, wantSet := range []string{
		`{"Name":"docs.example.com.","ResourceRecords":[{"Value":"192.0.2.10"}],"TTL":300,"Type":"A"}`,
		`{"Name":"example.com.","ResourceRecords":[{"Value":"\"v=spf1 -all\""}],"TTL":300,"Type":"TXT"}`,
	} {
		call := cli.calls[3+i]
		if cli.argAfter(call, "--hosted-zone-id") != "Z0CREATED" {
			t.Errorf("change %d zone id = %q, want the created zone", i, cli.argAfter(call, "--hosted-zone-id"))
		}
		var batch struct {
			Changes []struct {
				Action            string          `json:"Action"`
				ResourceRecordSet json.RawMessage `json:"ResourceRecordSet"`
			} `json:"Changes"`
		}
		if err := json.Unmarshal([]byte(cli.argAfter(call, "--change-batch")), &batch); err != nil || len(batch.Changes) != 1 {
			t.Fatalf("change %d batch = %q: %v", i, cli.argAfter(call, "--change-batch"), err)
		}
		if batch.Changes[0].Action != "UPSERT" || string(batch.Changes[0].ResourceRecordSet) != wantSet {
			t.Errorf("change %d = %s %s, want UPSERT %s", i, batch.Changes[0].Action, batch.Changes[0].ResourceRecordSet, wantSet)
		}
	}
}

// TestAwsPlansAgainstRecordedZone — AT3: a matching zone plans nothing, a
// TTL drift plans one update, an unmanaged record plans one delete carrying
// the live record, and the apex SOA and NS are never deleted. Alias record
// sets are invisible, and the wildcard escape is restored.
func TestAwsPlansAgainstRecordedZone(t *testing.T) {
	recorded := func() map[string][]fakeAwsResult {
		return map[string][]fakeAwsResult{
			"route53 list-hosted-zones-by-name":   {{out: awsZoneListJSON}},
			"route53 list-resource-record-sets":   {{out: awsRecordSetJSON}},
			"route53 change-resource-record-sets": {{out: `{}`}},
		}
	}
	provider, _ := newAwsFake(recorded())
	live, err := provider.ListDNS("example.com")
	if err != nil {
		t.Fatalf("ListDNS: %v", err)
	}
	var names []string
	for _, r := range live {
		names = append(names, r.Key())
	}
	wantNames := "example.com|A|*;example.com|A|docs;example.com|NS|@;example.com|SOA|@;example.com|TXT|@"
	if strings.Join(names, ";") != wantNames {
		t.Errorf("live keys = %v, want %s (alias skipped, wildcard restored)", names, wantNames)
	}
	for _, r := range live {
		if r.RecordType == "TXT" && r.Values[0] != "v=spf1 -all" {
			t.Errorf("TXT value = %q, want unquoted", r.Values[0])
		}
	}

	bundle := exampleDNSBundle()
	bundle.Dns = append(bundle.Dns, DnsRecord{Provider: "aws", Zone: "example.com", RecordType: "A", Name: "*", TTL: 300, Values: []string{"192.0.2.11"}})
	provider, _ = newAwsFake(recorded())
	changes, err := Plan(provider, bundle, &Options{Provider: "aws", PruneDNS: true})
	if err != nil || len(changes) != 0 {
		t.Fatalf("matching zone: changes=%v err=%v", changes, err)
	}

	bundle.Dns[0].TTL = 600
	provider, _ = newAwsFake(recorded())
	changes, err = Plan(provider, bundle, &Options{Provider: "aws", PruneDNS: true})
	if err != nil || len(changes) != 1 || changes[0].Action != ActionUpdate || changes[0].Key != "example.com|A|docs" {
		t.Fatalf("TTL drift: changes=%v err=%v", changes, err)
	}

	bundle.Dns = bundle.Dns[:2]
	bundle.Dns[0].TTL = 300
	provider, cli := newAwsFake(recorded())
	changes, err = Plan(provider, bundle, &Options{Provider: "aws", PruneDNS: true})
	if err != nil || len(changes) != 1 || changes[0].Action != ActionDelete || changes[0].Key != "example.com|A|*" {
		t.Fatalf("unmanaged record: changes=%v err=%v", changes, err)
	}
	if err := Apply(provider, changes, "", nil); err != nil {
		t.Fatalf("Apply delete: %v", err)
	}
	batch := cli.argAfter(cli.calls[len(cli.calls)-1], "--change-batch")
	if !strings.Contains(batch, `"Action":"DELETE"`) || !strings.Contains(batch, `"Name":"*.example.com."`) || !strings.Contains(batch, `"TTL":300`) || !strings.Contains(batch, `192.0.2.11`) {
		t.Errorf("DELETE batch = %s, want the live name, TTL, and value", batch)
	}
}

// TestAwsRunnerRefusesAndRedacts — AT4: a pair outside the allowlist is
// refused by name before the CLI runs, and a failing call is reduced to its
// command and exit status with no process output.
func TestAwsRunnerRefusesAndRedacts(t *testing.T) {
	provider, cli := newAwsFake(map[string][]fakeAwsResult{
		"route53 list-hosted-zones-by-name": {{out: "AccessDenied: arn:aws:iam::111111111111:user/example is not authorized", exit: 254}},
	})
	err := provider.run("iam", "list-users", nil)
	if err == nil || !strings.Contains(err.Error(), "`aws iam list-users`") || len(cli.calls) != 0 {
		t.Errorf("unlisted command: err=%v calls=%v", err, cli.calls)
	}
	_, err = provider.HasZone("example.com")
	if err == nil || err.Error() != "aws route53 list-hosted-zones-by-name failed with exit 254" {
		t.Errorf("failing call: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("redaction leaked process output: %v", err)
	}
}

func TestAwsNameAndValueMapping(t *testing.T) {
	cases := map[[2]string]string{{"@", "example.com"}: "example.com.", {"www", "example.com"}: "www.example.com.", {"", "example.com"}: "example.com."}
	for in, want := range cases {
		if got := absoluteName(in[0], in[1]); got != want {
			t.Errorf("absoluteName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	if got := relativeName("example.com.", "example.com"); got != "@" {
		t.Errorf("relativeName apex = %q", got)
	}
	if got := toRoute53Value("TXT", `say "hi"`); got != `"say \"hi\""` {
		t.Errorf("toRoute53Value = %q", got)
	}
	if got := fromRoute53Value("TXT", `"say \"hi\""`); got != `say "hi"` {
		t.Errorf("fromRoute53Value = %q", got)
	}
	if got := toRoute53Value("A", "192.0.2.1"); got != "192.0.2.1" {
		t.Errorf("non-TXT value changed: %q", got)
	}
}

// ---- Host kinds

func awsHostBundle() *Bundle {
	return &Bundle{
		IamRoles: []IamRole{{Provider: "aws", Name: "example-host",
			ManagedPolicies: []string{"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"},
			InlinePolicies:  map[string]any{"backup": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::example-attune-backups/*"}}}},
			InstanceProfile: true}},
		Buckets:   []Bucket{{Provider: "aws", Name: "example-attune-backups", Region: "us-east-1", PublicAccessBlock: PublicAccessBlock{true, true, true, true}, Encryption: "aes256"}},
		Firewalls: []Firewall{{Provider: "aws", Name: "example-host", Rules: []FirewallRule{{Protocol: "tcp", FromPort: 22, ToPort: 22, Sources: []string{"192.0.2.0/24"}}, {Protocol: "tcp", FromPort: 443, ToPort: 443, Sources: []string{"0.0.0.0/0"}}}}},
		Instances: []Instance{{Provider: "aws", Name: "example-host", Type: "t4g.small", Image: "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64",
			Role: "example-host", Firewall: "example-host", Imdsv2Required: true, RootDisk: RootDisk{Size: 20, Type: "gp3", Encrypted: true}, Tags: map[string]string{"project": "example"}}},
		Addresses: []StaticAddress{{Provider: "aws", Name: "example-host", Instance: "example-host"}},
	}
}

const (
	awsEmptyTaggingJSON = `{"ResourceTagMappingList":[]}`
	awsFullTaggingJSON  = `{"ResourceTagMappingList":[
		{"ResourceARN":"arn:aws:iam::111111111111:role/attune/example-host"},
		{"ResourceARN":"arn:aws:s3:::example-attune-backups"},
		{"ResourceARN":"arn:aws:ec2:us-east-1:111111111111:security-group/sg-0live"},
		{"ResourceARN":"arn:aws:ec2:us-east-1:111111111111:instance/i-0live"},
		{"ResourceARN":"arn:aws:ec2:us-east-1:111111111111:elastic-ip/eipalloc-0live"}]}`
	awsSecurityGroupJSON = `{"SecurityGroups":[{"GroupId":"sg-0live","GroupName":"example-host","VpcId":"vpc-0default","IpPermissions":[
		{"IpProtocol":"tcp","FromPort":22,"ToPort":22,"IpRanges":[{"CidrIp":"192.0.2.0/24"}]},
		{"IpProtocol":"tcp","FromPort":443,"ToPort":443,"IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]}]}`
	awsSecurityGroupMissingRuleJSON = `{"SecurityGroups":[{"GroupId":"sg-0live","GroupName":"example-host","VpcId":"vpc-0default","IpPermissions":[
		{"IpProtocol":"tcp","FromPort":22,"ToPort":22,"IpRanges":[{"CidrIp":"192.0.2.0/24"}]}]}]}`
)

func awsInstanceJSON(image, project string) string {
	return `{"Reservations":[{"Instances":[{"InstanceId":"i-0live","InstanceType":"t4g.small","ImageId":"` + image + `","State":{"Name":"running"},
		"IamInstanceProfile":{"Arn":"arn:aws:iam::111111111111:instance-profile/attune/example-host"},
		"SecurityGroups":[{"GroupId":"sg-0live","GroupName":"example-host"}],"MetadataOptions":{"HttpTokens":"required"},
		"RootDeviceName":"/dev/xvda","BlockDeviceMappings":[{"DeviceName":"/dev/xvda","Ebs":{"VolumeId":"vol-0live"}}],
		"Tags":[{"Key":"Name","Value":"example-host"},{"Key":"attune","Value":"managed"},{"Key":"project","Value":"` + project + `"}]}]}]}`
}

func awsFullAccount(groups, instances string) map[string][]fakeAwsResult {
	return map[string][]fakeAwsResult{
		"resourcegroupstaggingapi get-resources": {{out: awsFullTaggingJSON}},
		"iam list-attached-role-policies":        {{out: `{"AttachedPolicies":[{"PolicyArn":"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"}]}`}},
		"iam list-role-policies":                 {{out: `{"PolicyNames":["backup"]}`}},
		"iam get-role-policy":                    {{out: `{"PolicyDocument":{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::example-attune-backups/*"}]}}`}},
		"iam list-instance-profiles-for-role":    {{out: `{"InstanceProfiles":[{"InstanceProfileName":"example-host"}]}`}},
		"s3api get-bucket-location":              {{out: `{"LocationConstraint":null}`}},
		"s3api get-public-access-block":          {{out: `{"PublicAccessBlockConfiguration":{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}}`}},
		"s3api get-bucket-encryption":            {{out: `{"ServerSideEncryptionConfiguration":{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}}`}},
		"ec2 describe-security-groups":           {{out: groups}},
		"ec2 describe-vpcs":                      {{out: `{"Vpcs":[{"VpcId":"vpc-0default","IsDefault":true}]}`}},
		"ec2 describe-instances":                 {{out: instances}},
		"ec2 describe-volumes":                   {{out: `{"Volumes":[{"Size":20,"VolumeType":"gp3","Encrypted":true}]}`}},
		"ssm get-parameter":                      {{out: `{"Parameter":{"Value":"ami-0example"}}`}},
		"ec2 describe-addresses":                 {{out: `{"Addresses":[{"AllocationId":"eipalloc-0live","PublicIp":"203.0.113.10","InstanceId":"i-0live","AssociationId":"eipassoc-0live","Tags":[{"Key":"Name","Value":"example-host"}]}]}`}},
		"ec2 disassociate-address":               {{out: `{}`}},
		"ec2 release-address":                    {{out: `{}`}},
		"ec2 terminate-instances":                {{out: `{}`}},
		"ec2 delete-security-group":              {{out: `{}`}},
		"s3api delete-bucket":                    {{out: ``}},
		"iam remove-role-from-instance-profile":  {{out: ``}},
		"iam delete-instance-profile":            {{out: ``}},
		"iam detach-role-policy":                 {{out: ``}},
		"iam delete-role-policy":                 {{out: ``}},
		"iam delete-role":                        {{out: ``}},
	}
}

func changeKeys(changes []Change) []string {
	var keys []string
	for _, c := range changes {
		keys = append(keys, c.Action.String()+" "+c.Key)
	}
	return keys
}

// TestAwsCreatesHostInDependencyOrder — AT2: an empty account plans one
// create per spec in dependency order and apply drives the CLI in that
// order, with the instance profile, the resolved image, and the address
// shown in the applied line.
func TestAwsCreatesHostInDependencyOrder(t *testing.T) {
	provider, cli := newAwsFake(map[string][]fakeAwsResult{
		"resourcegroupstaggingapi get-resources": {{out: awsEmptyTaggingJSON}},
		"ssm get-parameter":                      {{out: `{"Parameter":{"Value":"ami-0example"}}`}},
		"route53 list-hosted-zones-by-name":      {{out: awsNoZoneJSON}},
		"route53 create-hosted-zone":             {{out: awsCreateZone}},
		"route53 change-resource-record-sets":    {{out: `{}`}},
		"iam create-role":                        {{out: `{"Role":{"Arn":"arn:aws:iam::111111111111:role/attune/example-host"}}`}},
		"iam attach-role-policy":                 {{out: ``}},
		"iam put-role-policy":                    {{out: ``}},
		"iam create-instance-profile":            {{out: `{}`}},
		"iam add-role-to-instance-profile":       {{out: ``}},
		"s3api create-bucket":                    {{out: `{"Location":"/example-attune-backups"}`}},
		"s3api put-bucket-tagging":               {{out: ``}},
		"s3api put-public-access-block":          {{out: ``}},
		"s3api put-bucket-encryption":            {{out: ``}},
		"ec2 describe-vpcs":                      {{out: `{"Vpcs":[{"VpcId":"vpc-0default","IsDefault":true}]}`}},
		"ec2 create-security-group":              {{out: `{"GroupId":"sg-0new"}`}},
		"ec2 authorize-security-group-ingress":   {{out: `{"Return":true}`}},
		"ec2 describe-images":                    {{out: `{"Images":[{"RootDeviceName":"/dev/xvda"}]}`}},
		"ec2 run-instances":                      {{out: `{"Instances":[{"InstanceId":"i-0new"}]}`}},
		"ec2 allocate-address":                   {{out: `{"AllocationId":"eipalloc-0new","PublicIp":"203.0.113.10"}`}},
		"ec2 associate-address":                  {{out: `{"AssociationId":"eipassoc-0new"}`}},
	})
	bundle := awsHostBundle()
	bundle.Dns = []DnsRecord{{Provider: "aws", Zone: "example.com", RecordType: "A", Name: "docs", TTL: 300, Values: []string{"192.0.2.10"}}}
	changes, err := Plan(provider, bundle, &Options{Provider: "aws", PruneDNS: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []string{"create iamRole|example-host", "create bucket|example-attune-backups", "create firewall|example-host", "create instance|example-host", "create staticAddress|example-host", "create dnsZone|example.com", "create example.com|A|docs"}
	if got := changeKeys(changes); strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("changes = %v\nwant %v", got, want)
	}
	if changes[3].Target.Instance.Image != "ami-0example" {
		t.Errorf("instance image = %q, want the resolved id", changes[3].Target.Instance.Image)
	}
	var applied []string
	if err := Apply(provider, changes, "", func(c Change) { applied = append(applied, c.Key+" "+c.Summary) }); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	wantPairs := []string{
		"resourcegroupstaggingapi get-resources", "ssm get-parameter", "route53 list-hosted-zones-by-name",
		"iam create-role", "iam attach-role-policy", "iam put-role-policy", "iam create-instance-profile", "iam add-role-to-instance-profile",
		"s3api create-bucket", "s3api put-bucket-tagging", "s3api put-public-access-block", "s3api put-bucket-encryption",
		"ec2 describe-vpcs", "ec2 create-security-group", "ec2 authorize-security-group-ingress",
		"ec2 describe-images", "ec2 run-instances",
		"ec2 allocate-address", "ec2 associate-address",
		"route53 list-hosted-zones-by-name", "route53 create-hosted-zone", "route53 change-resource-record-sets",
	}
	if got := cli.pairs(); strings.Join(got, ";") != strings.Join(wantPairs, ";") {
		t.Fatalf("calls = %v\nwant %v", got, wantPairs)
	}
	var run []string
	for _, call := range cli.calls {
		if call[1] == "run-instances" {
			run = call
		}
	}
	for _, want := range []string{`{"Name":"example-host"}`, "sg-0new", "ami-0example", `"HttpTokens":"required"`, `"VolumeSize":20`, `"Encrypted":true`} {
		if !strings.Contains(strings.Join(run, " "), want) {
			t.Errorf("run-instances argv lacks %q: %v", want, run)
		}
	}
	if !slices.Contains(applied, "staticAddress|example-host address 203.0.113.10") {
		t.Errorf("applied lines = %v, want the address shown", applied)
	}
}

// TestAwsPlansAgainstRecordedHost — AT3: a matching account plans nothing;
// a missing rule and a changed tag plan exactly two updates with diffs that
// name them.
func TestAwsPlansAgainstRecordedHost(t *testing.T) {
	provider, _ := newAwsFake(awsFullAccount(awsSecurityGroupJSON, awsInstanceJSON("ami-0example", "example")))
	changes, err := Plan(provider, awsHostBundle(), &Options{Provider: "aws", PruneHosts: true})
	if err != nil || len(changes) != 0 {
		t.Fatalf("matching account: changes=%v err=%v", changeKeys(changes), err)
	}
	provider, _ = newAwsFake(awsFullAccount(awsSecurityGroupMissingRuleJSON, awsInstanceJSON("ami-0example", "old")))
	changes, err = Plan(provider, awsHostBundle(), &Options{Provider: "aws"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := changeKeys(changes); strings.Join(got, ";") != "update firewall|example-host;update instance|example-host" {
		t.Fatalf("changes = %v", got)
	}
	if d := changes[0].Diffs; len(d) != 1 || d[0].Field != "rule added" || d[0].New != "tcp|443|443|0.0.0.0/0" {
		t.Errorf("firewall diffs = %+v", d)
	}
	if d := changes[1].Diffs; len(d) != 1 || d[0].Field != "tag project" || d[0].Old != "old" || d[0].New != "example" {
		t.Errorf("instance diffs = %+v", d)
	}
}

// TestAwsReplaceIsPlannedAndRefused — AT4: a live instance on another image
// plans a replace naming the field, and Apply refuses it before any
// mutation.
func TestAwsReplaceIsPlannedAndRefused(t *testing.T) {
	provider, cli := newAwsFake(awsFullAccount(awsSecurityGroupJSON, awsInstanceJSON("ami-0old", "example")))
	changes, err := Plan(provider, awsHostBundle(), &Options{Provider: "aws"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := changeKeys(changes); strings.Join(got, ";") != "replace instance|example-host" || changes[0].Summary != "image is immutable" {
		t.Fatalf("changes = %v summary=%q", got, changes[0].Summary)
	}
	before := len(cli.calls)
	err = Apply(provider, changes, "", nil)
	want := "replace instance instance|example-host: image is immutable; remove the spec, apply, then add it back"
	if err == nil || err.Error() != want {
		t.Errorf("Apply = %v, want %q", err, want)
	}
	if len(cli.calls) != before {
		t.Errorf("Apply ran %d CLI calls after refusing", len(cli.calls)-before)
	}
	if plain := color.ClearCode(renderPlanBlock("aws", changes, false)); !strings.Contains(plain, "! replace instance") {
		t.Errorf("plan block = %q, want a ! replace line", plain)
	}
}

// TestAwsPrunesHostsInReverseOrder — AT5: unmanaged live resources are left
// alone by default and, with host pruning on, deleted address first and
// role last, the address detached before release and release before
// termination.
func TestAwsPrunesHostsInReverseOrder(t *testing.T) {
	other := awsHostBundle()
	other.IamRoles[0].Name, other.Buckets[0].Name, other.Firewalls[0].Name = "other", "other-backups", "other"
	other.Instances[0].Name, other.Instances[0].Role, other.Instances[0].Firewall = "other", "other", "other"
	other.Addresses[0].Name, other.Addresses[0].Instance = "other", "other"
	provider, _ := newAwsFake(awsFullAccount(awsSecurityGroupJSON, awsInstanceJSON("ami-0example", "example")))
	changes, err := Plan(provider, other, &Options{Provider: "aws"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, c := range changes {
		if c.Action == ActionDelete {
			t.Fatalf("delete planned without host pruning: %v", changeKeys(changes))
		}
	}
	provider, cli := newAwsFake(awsFullAccount(awsSecurityGroupJSON, awsInstanceJSON("ami-0example", "example")))
	changes, err = Plan(provider, other, &Options{Provider: "aws", PruneHosts: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var deletes []Change
	for _, c := range changes {
		if c.Action == ActionDelete {
			deletes = append(deletes, c)
		}
	}
	want := "delete staticAddress|example-host;delete instance|example-host;delete firewall|example-host;delete bucket|example-attune-backups;delete iamRole|example-host"
	if got := changeKeys(deletes); strings.Join(got, ";") != want {
		t.Fatalf("deletes = %v\nwant %s", got, want)
	}
	before := len(cli.calls)
	if err := Apply(provider, deletes, "", nil); err != nil {
		t.Fatalf("Apply deletes: %v", err)
	}
	var mutations []string
	for _, call := range cli.calls[before:] {
		if !strings.HasPrefix(call[1], "describe") && !strings.HasPrefix(call[1], "list") {
			mutations = append(mutations, call[0]+" "+call[1])
		}
	}
	wantMutations := []string{"ec2 disassociate-address", "ec2 release-address", "ec2 terminate-instances", "ec2 delete-security-group", "s3api delete-bucket",
		"iam remove-role-from-instance-profile", "iam delete-instance-profile", "iam detach-role-policy", "iam delete-role-policy", "iam delete-role"}
	if strings.Join(mutations, ";") != strings.Join(wantMutations, ";") {
		t.Errorf("mutations = %v\nwant %v", mutations, wantMutations)
	}
}

// TestAwsHostAllowlistStillRefusesOthers — AT6: the grown allowlist still
// refuses a pair outside it.
func TestAwsHostAllowlistStillRefusesOthers(t *testing.T) {
	provider, cli := newAwsFake(nil)
	err := provider.run("ec2", "describe-key-pairs", nil)
	if err == nil || !strings.Contains(err.Error(), "`aws ec2 describe-key-pairs`") || len(cli.calls) != 0 {
		t.Errorf("unlisted command: err=%v calls=%v", err, cli.calls)
	}
	spec, _ := LookupProvider("aws")
	if !slices.Equal(spec.Kinds, []string{"dnsRecordSet", "iamRole", "bucket", "firewall", "instance", "staticAddress"}) {
		t.Errorf("aws kinds = %v", spec.Kinds)
	}
}
