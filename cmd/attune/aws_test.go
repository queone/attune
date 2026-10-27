package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if err := Apply(provider, changes, &Options{}, nil); err != nil {
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
	if err := Apply(provider, changes, &Options{}, nil); err != nil {
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
		{"ResourceARN":"arn:aws:iam::111111111111:instance-profile/attune/example-host"},
		{"ResourceARN":"arn:aws:ec2:us-east-1:111111111111:security-group/sg-0live"},
		{"ResourceARN":"arn:aws:ec2:us-east-1:111111111111:instance/i-0live"},
		{"ResourceARN":"arn:aws:ec2:us-east-1:111111111111:elastic-ip/eipalloc-0live"}]}`
	awsRoleListJSON      = `{"Roles":[{"RoleName":"example-host","Path":"/attune/","Arn":"arn:aws:iam::111111111111:role/attune/example-host"},{"RoleName":"other","Path":"/","Arn":"arn:aws:iam::111111111111:role/other"}]}`
	awsBucketListJSON    = `{"Buckets":[{"Name":"example-attune-backups"}]}`
	awsBucketTagsJSON    = `{"TagSet":[{"Key":"attune","Value":"managed"}]}`
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
		"iam list-roles":                         {{out: awsRoleListJSON}},
		"s3api list-buckets":                     {{out: awsBucketListJSON}},
		"s3api get-bucket-tagging":               {{out: awsBucketTagsJSON}},
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
		"iam list-roles":                         {{out: `{"Roles":[]}`}},
		"s3api list-buckets":                     {{out: `{"Buckets":[]}`}},
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
		"ec2 describe-instances":                 {{out: `{"Reservations":[{"Instances":[{"InstanceId":"i-0new","State":{"Name":"running"},"Tags":[{"Key":"Name","Value":"example-host"}]}]}]}`}},
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
	if err := Apply(provider, changes, &Options{}, func(c Change) { applied = append(applied, c.Key+" "+c.Summary) }); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	wantPairs := []string{
		"iam list-roles", "s3api list-buckets", "resourcegroupstaggingapi get-resources", "ssm get-parameter", "route53 list-hosted-zones-by-name",
		"iam create-role", "iam attach-role-policy", "iam put-role-policy", "iam create-instance-profile", "iam add-role-to-instance-profile",
		"s3api create-bucket", "s3api put-bucket-tagging", "s3api put-public-access-block", "s3api put-bucket-encryption",
		"ec2 describe-vpcs", "ec2 create-security-group", "ec2 authorize-security-group-ingress",
		"ec2 describe-images", "ec2 run-instances",
		"ec2 describe-instances", "ec2 allocate-address", "ec2 associate-address",
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
	err = Apply(provider, changes, &Options{}, nil)
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
	if err := Apply(provider, deletes, &Options{}, nil); err != nil {
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
	if !slices.Equal(spec.Kinds, []string{"dnsRecordSet", "iamRole", "bucket", "firewall", "instance", "staticAddress", "budget", "healthCheck"}) {
		t.Errorf("aws kinds = %v", spec.Kinds)
	}
}

// ---- References

const awsReference = "${staticAddress:example-host:publicIp}"

func awsZoneRecordsJSON(ip string) string {
	return `{"ResourceRecordSets":[
		{"Name":"example.com.","Type":"NS","TTL":172800,"ResourceRecords":[{"Value":"ns-1.awsdns-00.com."}]},
		{"Name":"example.com.","Type":"SOA","TTL":900,"ResourceRecords":[{"Value":"ns-1.awsdns-00.com. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400"}]},
		{"Name":"docs.example.com.","Type":"A","TTL":300,"ResourceRecords":[{"Value":"` + ip + `"}]}]}`
}

func awsReferenceBundle() *Bundle {
	bundle := awsHostBundle()
	bundle.Dns = []DnsRecord{{Provider: "aws", Zone: "example.com", RecordType: "A", Name: "docs", TTL: 300, Values: []string{awsReference}}}
	return bundle
}

func awsFullAccountWithZone(ip string) map[string][]fakeAwsResult {
	recorded := awsFullAccount(awsSecurityGroupJSON, awsInstanceJSON("ami-0example", "example"))
	recorded["route53 list-hosted-zones-by-name"] = []fakeAwsResult{{out: awsZoneListJSON}}
	recorded["route53 list-resource-record-sets"] = []fakeAwsResult{{out: awsZoneRecordsJSON(ip)}}
	recorded["route53 change-resource-record-sets"] = []fakeAwsResult{{out: `{}`}}
	return recorded
}

// TestAwsReferenceResolvesAfterTheAddressIsCreated — AT2: on an empty
// account the record plans as awaiting the address and apply sends the
// allocated address in the record batch.
func TestAwsReferenceResolvesAfterTheAddressIsCreated(t *testing.T) {
	provider, cli := newAwsFake(map[string][]fakeAwsResult{
		"resourcegroupstaggingapi get-resources": {{out: awsEmptyTaggingJSON}},
		"iam list-roles":                         {{out: `{"Roles":[]}`}},
		"s3api list-buckets":                     {{out: `{"Buckets":[]}`}},
		"ssm get-parameter":                      {{out: `{"Parameter":{"Value":"ami-0example"}}`}},
		"route53 list-hosted-zones-by-name":      {{out: awsZoneListJSON}},
		"route53 list-resource-record-sets":      {{out: `{"ResourceRecordSets":[]}`}},
		"route53 change-resource-record-sets":    {{out: `{}`}},
		"iam create-role":                        {{out: `{}`}},
		"iam attach-role-policy":                 {{out: ``}},
		"iam put-role-policy":                    {{out: ``}},
		"iam create-instance-profile":            {{out: `{}`}},
		"iam add-role-to-instance-profile":       {{out: ``}},
		"s3api create-bucket":                    {{out: `{}`}},
		"s3api put-bucket-tagging":               {{out: ``}},
		"s3api put-public-access-block":          {{out: ``}},
		"s3api put-bucket-encryption":            {{out: ``}},
		"ec2 describe-vpcs":                      {{out: `{"Vpcs":[{"VpcId":"vpc-0default","IsDefault":true}]}`}},
		"ec2 create-security-group":              {{out: `{"GroupId":"sg-0new"}`}},
		"ec2 authorize-security-group-ingress":   {{out: `{}`}},
		"ec2 describe-images":                    {{out: `{"Images":[{"RootDeviceName":"/dev/xvda"}]}`}},
		"ec2 run-instances":                      {{out: `{"Instances":[{"InstanceId":"i-0new"}]}`}},
		"ec2 describe-instances":                 {{out: `{"Reservations":[{"Instances":[{"InstanceId":"i-0new","State":{"Name":"running"},"Tags":[{"Key":"Name","Value":"example-host"}]}]}]}`}},
		"ec2 allocate-address":                   {{out: `{"AllocationId":"eipalloc-0new","PublicIp":"203.0.113.10"}`}},
		"ec2 associate-address":                  {{out: `{}`}},
	})
	options := &Options{Provider: "aws", PruneDNS: true}
	changes, err := Plan(provider, awsReferenceBundle(), options)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	keys := changeKeys(changes)
	if len(keys) != 6 || keys[4] != "create staticAddress|example-host" || keys[5] != "create example.com|A|docs" {
		t.Fatalf("changes = %v", keys)
	}
	if changes[5].Summary != "awaits "+awsReference {
		t.Errorf("record summary = %q", changes[5].Summary)
	}
	if err := Apply(provider, changes, options, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	last := cli.calls[len(cli.calls)-1]
	if last[1] != "change-resource-record-sets" || !strings.Contains(cli.argAfter(last, "--change-batch"), `"Value":"203.0.113.10"`) {
		t.Errorf("last call = %v, want the record batch carrying the allocated address", last)
	}
	if id := options.Outputs.values["${instance:example-host:id}"]; id != "i-0new" {
		t.Errorf("instance output = %q, want the launched id", id)
	}
}

// TestAwsReferenceResolvesFromLiveState — AT3 and AT4: a live record that
// already holds the live address plans no change, a stale one plans one
// update whose diff shows the resolved address, and -k dnsRecordSet still
// reads the address to resolve the reference.
func TestAwsReferenceResolvesFromLiveState(t *testing.T) {
	provider, _ := newAwsFake(awsFullAccountWithZone("203.0.113.10"))
	changes, err := Plan(provider, awsReferenceBundle(), &Options{Provider: "aws", PruneDNS: true})
	if err != nil || len(changes) != 0 {
		t.Fatalf("matching: changes=%v err=%v", changeKeys(changes), err)
	}
	provider, _ = newAwsFake(awsFullAccountWithZone("203.0.113.99"))
	changes, err = Plan(provider, awsReferenceBundle(), &Options{Provider: "aws", PruneDNS: true})
	if err != nil || len(changes) != 1 || changes[0].Action != ActionUpdate {
		t.Fatalf("stale: changes=%v err=%v", changeKeys(changes), err)
	}
	if d := changes[0].Diffs; len(d) != 1 || d[0].Field != "values" || d[0].Old != "203.0.113.99" || d[0].New != "203.0.113.10" {
		t.Errorf("diffs = %+v", d)
	}
	if changes[0].Target.Dns.Values[0] != "203.0.113.10" {
		t.Errorf("target value = %q, want resolved", changes[0].Target.Dns.Values[0])
	}
	provider, cli := newAwsFake(awsFullAccountWithZone("203.0.113.10"))
	changes, err = Plan(provider, awsReferenceBundle(), &Options{Provider: "aws", PruneDNS: true, Kind: "dnsRecordSet"})
	if err != nil || len(changes) != 0 {
		t.Fatalf("-k dnsRecordSet: changes=%v err=%v", changeKeys(changes), err)
	}
	if !slices.Contains(cli.pairs(), "ec2 describe-addresses") {
		t.Errorf("calls = %v, want the address read despite the kind filter", cli.pairs())
	}
}

// TestAwsUnresolvedReferenceFailsBeforeDNS — AT5: a record whose reference
// has no output fails apply before any DNS call.
func TestAwsUnresolvedReferenceFailsBeforeDNS(t *testing.T) {
	provider, cli := newAwsFake(map[string][]fakeAwsResult{"route53 list-hosted-zones-by-name": {{out: awsZoneListJSON}}})
	record := DnsRecord{Provider: "aws", Zone: "example.com", RecordType: "A", Name: "docs", TTL: 300, Values: []string{awsReference}}
	err := Apply(provider, []Change{{Kind: "dnsRecordSet", Action: ActionCreate, Key: record.Key(), Target: Target{Kind: TargetDns, Dns: record}}}, &Options{Provider: "aws"}, nil)
	want := "reference " + awsReference + " is not resolved; apply its target first"
	if err == nil || err.Error() != want {
		t.Errorf("Apply = %v, want %q", err, want)
	}
	if len(cli.calls) != 0 {
		t.Errorf("DNS calls made before the refusal: %v", cli.pairs())
	}
}

// ---- Monitoring

func awsMonitoringBundle() *Bundle {
	bundle := awsReferenceBundle()
	bundle.Budgets = []Budget{{Provider: "aws", Name: "example-monthly", Amount: "15.00", Currency: "USD", Alerts: []BudgetAlert{
		{Threshold: 80, Emails: []string{"ops@example.com"}},
		{Threshold: 100, Forecast: true, Emails: []string{"ops@example.com"}},
	}}}
	bundle.HealthChecks = []HealthCheck{{Provider: "aws", Name: "example-site", Target: awsReference, Protocol: "HTTPS", Port: 443, Path: "/", Host: "example.com",
		Interval: 30, FailureThreshold: 3, Emails: []string{"ops@example.com"}, Alarm: true}}
	return bundle
}

const (
	awsTopicARN            = "arn:aws:sns:us-east-1:111111111111:attune-example-site"
	awsBudgetNotifications = `{"Notifications":[{"NotificationType":"ACTUAL","ComparisonOperator":"GREATER_THAN","Threshold":80.0,"ThresholdType":"PERCENTAGE"},{"NotificationType":"FORECASTED","ComparisonOperator":"GREATER_THAN","Threshold":100.0,"ThresholdType":"PERCENTAGE"}]}`
)

const awsHealthCheckTagsJSON = `{"ResourceTagSets":[
	{"ResourceType":"healthcheck","ResourceId":"hc-live","Tags":[{"Key":"attune","Value":"managed"},{"Key":"Name","Value":"example-site"}]},
	{"ResourceType":"healthcheck","ResourceId":"hc-other","Tags":[{"Key":"Name","Value":"other"}]}]}`

func awsHealthCheckJSON(protocol string, threshold int) string {
	return `{"HealthChecks":[{"Id":"hc-live","HealthCheckConfig":{"IPAddress":"203.0.113.10","Port":443,"Type":"` + protocol + `","ResourcePath":"/","FullyQualifiedDomainName":"example.com","RequestInterval":30,"FailureThreshold":` + string(rune('0'+threshold)) + `}},
		{"Id":"hc-other","HealthCheckConfig":{"IPAddress":"203.0.113.11","Port":80,"Type":"HTTP","ResourcePath":"/","RequestInterval":30,"FailureThreshold":3}}]}`
}

func awsFullAccountWithMonitoring(notifications, healthCheck string) map[string][]fakeAwsResult {
	recorded := awsFullAccountWithZone("203.0.113.10")
	recorded["sts get-caller-identity"] = []fakeAwsResult{{out: awsIdentityJSON}}
	recorded["budgets describe-budgets"] = []fakeAwsResult{{out: `{"Budgets":[{"BudgetName":"example-monthly","BudgetLimit":{"Amount":"15.0","Unit":"USD"},"TimeUnit":"MONTHLY","BudgetType":"COST"}]}`}}
	recorded["budgets list-tags-for-resource"] = []fakeAwsResult{{out: `{"ResourceTags":[{"Key":"attune","Value":"managed"}]}`}}
	recorded["budgets describe-notifications-for-budget"] = []fakeAwsResult{{out: notifications}}
	recorded["budgets describe-subscribers-for-notification"] = []fakeAwsResult{{out: `{"Subscribers":[{"SubscriptionType":"EMAIL","Address":"ops@example.com"}]}`}}
	recorded["route53 list-health-checks"] = []fakeAwsResult{{out: healthCheck}}
	recorded["route53 list-tags-for-resources"] = []fakeAwsResult{{out: awsHealthCheckTagsJSON}}
	recorded["sns list-topics"] = []fakeAwsResult{{out: `{"Topics":[{"TopicArn":"` + awsTopicARN + `"}]}`}}
	recorded["sns list-subscriptions-by-topic"] = []fakeAwsResult{{out: `{"Subscriptions":[{"Endpoint":"ops@example.com","Protocol":"email","SubscriptionArn":"` + awsTopicARN + `:1"}]}`}}
	recorded["cloudwatch describe-alarms"] = []fakeAwsResult{{out: `{"MetricAlarms":[{"AlarmName":"attune-example-site"}]}`}}
	for _, pair := range []string{"budgets delete-notification", "budgets create-notification", "budgets delete-budget", "budgets update-budget",
		"route53 update-health-check", "route53 delete-health-check", "sns create-topic", "sns delete-topic", "cloudwatch put-metric-alarm", "cloudwatch delete-alarms"} {
		out := `{}`
		if pair == "sns create-topic" {
			out = `{"TopicArn":"` + awsTopicARN + `"}`
		}
		recorded[pair] = []fakeAwsResult{{out: out}}
	}
	return recorded
}

func grounded(t *testing.T, provider *AwsProvider) {
	t.Helper()
	if _, err := provider.Ground(); err != nil {
		t.Fatalf("Ground: %v", err)
	}
}

// TestAwsCreatesBudgetAndHealthCheck — AT2: an empty account plans the
// budget first and the check after the address awaiting its reference, and
// apply creates the budget with its alerts and tag, then the check on the
// allocated address with its topic, subscription, and alarm in us-east-1.
func TestAwsCreatesBudgetAndHealthCheck(t *testing.T) {
	recorded := map[string][]fakeAwsResult{
		"sts get-caller-identity":                {{out: awsIdentityJSON}},
		"resourcegroupstaggingapi get-resources": {{out: awsEmptyTaggingJSON}},
		"iam list-roles":                         {{out: `{"Roles":[]}`}},
		"s3api list-buckets":                     {{out: `{"Buckets":[]}`}},
		"route53 list-health-checks":             {{out: `{"HealthChecks":[]}`}},
		"budgets describe-budgets":               {{out: `{"Budgets":[]}`}},
		"budgets create-budget":                  {{out: ``}},
		"ssm get-parameter":                      {{out: `{"Parameter":{"Value":"ami-0example"}}`}},
		"route53 list-hosted-zones-by-name":      {{out: awsZoneListJSON}},
		"route53 list-resource-record-sets":      {{out: `{"ResourceRecordSets":[]}`}},
		"route53 change-resource-record-sets":    {{out: `{}`}},
		"route53 create-health-check":            {{out: `{"HealthCheck":{"Id":"hc-new"}}`}},
		"route53 change-tags-for-resource":       {{out: ``}},
		"sns create-topic":                       {{out: `{"TopicArn":"` + awsTopicARN + `"}`}},
		"sns list-subscriptions-by-topic":        {{out: `{"Subscriptions":[]}`}},
		"sns subscribe":                          {{out: `{"SubscriptionArn":"pending confirmation"}`}},
		"cloudwatch put-metric-alarm":            {{out: ``}},
		"iam create-role":                        {{out: `{}`}},
		"iam attach-role-policy":                 {{out: ``}},
		"iam put-role-policy":                    {{out: ``}},
		"iam create-instance-profile":            {{out: `{}`}},
		"iam add-role-to-instance-profile":       {{out: ``}},
		"s3api create-bucket":                    {{out: `{}`}},
		"s3api put-bucket-tagging":               {{out: ``}},
		"s3api put-public-access-block":          {{out: ``}},
		"s3api put-bucket-encryption":            {{out: ``}},
		"ec2 describe-vpcs":                      {{out: `{"Vpcs":[{"VpcId":"vpc-0default","IsDefault":true}]}`}},
		"ec2 create-security-group":              {{out: `{"GroupId":"sg-0new"}`}},
		"ec2 authorize-security-group-ingress":   {{out: `{}`}},
		"ec2 describe-images":                    {{out: `{"Images":[{"RootDeviceName":"/dev/xvda"}]}`}},
		"ec2 run-instances":                      {{out: `{"Instances":[{"InstanceId":"i-0new"}]}`}},
		"ec2 describe-instances":                 {{out: `{"Reservations":[{"Instances":[{"InstanceId":"i-0new","State":{"Name":"running"},"Tags":[{"Key":"Name","Value":"example-host"}]}]}]}`}},
		"ec2 allocate-address":                   {{out: `{"AllocationId":"eipalloc-0new","PublicIp":"203.0.113.10"}`}},
		"ec2 associate-address":                  {{out: `{}`}},
	}
	provider, cli := newAwsFake(recorded)
	provider.region = "us-east-2"
	grounded(t, provider)
	options := &Options{Provider: "aws", PruneDNS: true}
	changes, err := Plan(provider, awsMonitoringBundle(), options)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	keys := changeKeys(changes)
	want := []string{"create budget|example-monthly", "create iamRole|example-host", "create bucket|example-attune-backups", "create firewall|example-host",
		"create instance|example-host", "create staticAddress|example-host", "create healthCheck|example-site", "create example.com|A|docs"}
	if strings.Join(keys, ";") != strings.Join(want, ";") {
		t.Fatalf("changes = %v\nwant %v", keys, want)
	}
	if changes[6].Summary != "awaits "+awsReference {
		t.Errorf("health check summary = %q", changes[6].Summary)
	}
	if err := Apply(provider, changes, options, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var budgetCall, checkCall, alarmCall, subscribeCalls []string
	subscribeCount := 0
	for _, call := range cli.calls {
		switch call[0] + " " + call[1] {
		case "budgets create-budget":
			budgetCall = call
		case "route53 create-health-check":
			checkCall = call
		case "cloudwatch put-metric-alarm":
			alarmCall = call
		case "sns subscribe":
			subscribeCount++
			subscribeCalls = call
		}
	}
	joined := strings.Join(budgetCall, " ")
	for _, want := range []string{`"Amount":"15.00"`, `"NotificationType":"ACTUAL"`, `"NotificationType":"FORECASTED"`, `"Threshold":80`, `"Key":"attune"`, "--account-id 111111111111"} {
		if !strings.Contains(joined, want) {
			t.Errorf("create-budget argv lacks %q: %v", want, budgetCall)
		}
	}
	config := cli.argAfter(checkCall, "--health-check-config")
	for _, want := range []string{`"IPAddress":"203.0.113.10"`, `"Port":443`, `"Type":"HTTPS"`, `"FullyQualifiedDomainName":"example.com"`} {
		if !strings.Contains(config, want) {
			t.Errorf("health check config lacks %q: %s", want, config)
		}
	}
	if subscribeCount != 1 || cli.argAfter(subscribeCalls, "--notification-endpoint") != "ops@example.com" {
		t.Errorf("subscribe calls = %d %v", subscribeCount, subscribeCalls)
	}
	if cli.argAfter(alarmCall, "--region") != "us-east-1" || cli.argAfter(alarmCall, "--metric-name") != "HealthCheckStatus" ||
		cli.argAfter(alarmCall, "--alarm-actions") != awsTopicARN || cli.argAfter(alarmCall, "--ok-actions") != awsTopicARN || !strings.Contains(cli.argAfter(alarmCall, "--dimensions"), "hc-new") {
		t.Errorf("put-metric-alarm argv = %v", alarmCall)
	}
	if cli.argAfter(checkCall, "--region") != "us-east-2" {
		t.Errorf("create-health-check region = %q, want the configured us-east-2", cli.argAfter(checkCall, "--region"))
	}
	alertChainStaysInAlarmRegion(t, cli, 0)
	order := cli.pairs()
	if slices.Index(order, "route53 create-health-check") < slices.Index(order, "ec2 associate-address") || slices.Index(order, "budgets create-budget") > slices.Index(order, "iam create-role") {
		t.Errorf("call order = %v", order)
	}
}

// TestAwsPlansMonitoringAgainstRecordedAccount — AT3: a matching account
// plans nothing; a changed alert threshold and failure threshold plan two
// updates with diffs naming them, and apply swaps the notification and
// updates the check.
func TestAwsPlansMonitoringAgainstRecordedAccount(t *testing.T) {
	provider, cli := newAwsFake(awsFullAccountWithMonitoring(awsBudgetNotifications, awsHealthCheckJSON("HTTPS", 3)))
	provider.region = "us-east-2"
	grounded(t, provider)
	changes, err := Plan(provider, awsMonitoringBundle(), &Options{Provider: "aws", PruneDNS: true, PruneMonitoring: true})
	if err != nil || len(changes) != 0 {
		t.Fatalf("matching: changes=%v err=%v", changeKeys(changes), err)
	}
	alertChainStaysInAlarmRegion(t, cli, 0)
	drifted := strings.Replace(awsBudgetNotifications, `"Threshold":80.0`, `"Threshold":70.0`, 1)
	provider, cli = newAwsFake(awsFullAccountWithMonitoring(drifted, awsHealthCheckJSON("HTTPS", 2)))
	provider.region = "us-east-2"
	grounded(t, provider)
	options := &Options{Provider: "aws", PruneDNS: true}
	changes, err = Plan(provider, awsMonitoringBundle(), options)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := changeKeys(changes); strings.Join(got, ";") != "update budget|example-monthly;update healthCheck|example-site" {
		t.Fatalf("changes = %v", got)
	}
	if d := changes[0].Diffs; len(d) != 2 || d[0].Field != "alert added" || !strings.HasPrefix(d[0].New, "actual|80|") || d[1].Field != "alert removed" {
		t.Errorf("budget diffs = %+v", d)
	}
	if d := changes[1].Diffs; len(d) != 1 || d[0].Field != "failureThreshold" || d[0].Old != "2" || d[0].New != "3" {
		t.Errorf("health check diffs = %+v", d)
	}
	before := len(cli.calls)
	if err := Apply(provider, changes, options, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var mutations []string
	for _, call := range cli.calls[before:] {
		if !strings.HasPrefix(call[1], "describe") && !strings.HasPrefix(call[1], "list") {
			mutations = append(mutations, call[0]+" "+call[1])
		}
	}
	for _, want := range []string{"budgets delete-notification", "budgets create-notification", "route53 update-health-check", "cloudwatch put-metric-alarm"} {
		if !slices.Contains(mutations, want) {
			t.Errorf("mutations %v lack %s", mutations, want)
		}
	}
	if slices.Contains(mutations, "budgets update-budget") || slices.Contains(mutations, "sns subscribe") {
		t.Errorf("unneeded mutations: %v", mutations)
	}
	alertChainStaysInAlarmRegion(t, cli, before)
}

// TestAwsHealthCheckProtocolChangeIsReplace — AT4: a live check on another
// protocol plans a replace and apply refuses it.
func TestAwsHealthCheckProtocolChangeIsReplace(t *testing.T) {
	provider, cli := newAwsFake(awsFullAccountWithMonitoring(awsBudgetNotifications, awsHealthCheckJSON("HTTP", 3)))
	grounded(t, provider)
	options := &Options{Provider: "aws", PruneDNS: true}
	changes, err := Plan(provider, awsMonitoringBundle(), options)
	if err != nil || len(changes) != 1 || changes[0].Action != ActionReplace || changes[0].Summary != "protocol is immutable" {
		t.Fatalf("changes=%v err=%v", changeKeys(changes), err)
	}
	before := len(cli.calls)
	if err := Apply(provider, changes, options, nil); err == nil || !strings.HasPrefix(err.Error(), "replace healthCheck healthCheck|example-site: protocol is immutable") {
		t.Errorf("Apply = %v", err)
	}
	if len(cli.calls) != before {
		t.Error("Apply ran CLI calls after refusing")
	}
}

// TestAwsPrunesMonitoringOnlyWhenAsked — AT5: unmanaged live monitoring is
// left alone by default and, with -M, deleted check first then budget, the
// alarm and topic going before the check.
func TestAwsPrunesMonitoringOnlyWhenAsked(t *testing.T) {
	other := awsMonitoringBundle()
	other.Budgets[0].Name = "other-monthly"
	other.HealthChecks[0].Name = "other-site"
	provider, _ := newAwsFake(awsFullAccountWithMonitoring(awsBudgetNotifications, awsHealthCheckJSON("HTTPS", 3)))
	grounded(t, provider)
	changes, err := Plan(provider, other, &Options{Provider: "aws", PruneDNS: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, c := range changes {
		if c.Action == ActionDelete {
			t.Fatalf("delete planned without monitoring pruning: %v", changeKeys(changes))
		}
	}
	provider, cli := newAwsFake(awsFullAccountWithMonitoring(awsBudgetNotifications, awsHealthCheckJSON("HTTPS", 3)))
	provider.region = "us-east-2"
	grounded(t, provider)
	options := &Options{Provider: "aws", PruneDNS: true, PruneMonitoring: true}
	changes, err = Plan(provider, other, options)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var deletes []Change
	for _, c := range changes {
		if c.Action == ActionDelete {
			deletes = append(deletes, c)
		}
	}
	if got := changeKeys(deletes); strings.Join(got, ";") != "delete healthCheck|example-site;delete budget|example-monthly" {
		t.Fatalf("deletes = %v", got)
	}
	before := len(cli.calls)
	if err := Apply(provider, deletes, options, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var mutations []string
	for _, call := range cli.calls[before:] {
		if !strings.HasPrefix(call[1], "describe") && !strings.HasPrefix(call[1], "list") {
			mutations = append(mutations, call[0]+" "+call[1])
			if (call[0] == "cloudwatch" || call[0] == "sns") && cli.argAfter(call, "--region") != "us-east-1" {
				t.Errorf("%s ran outside us-east-1: %v", call[1], call)
			}
		}
	}
	want := "cloudwatch delete-alarms;sns delete-topic;route53 delete-health-check;budgets delete-budget"
	if strings.Join(mutations, ";") != want {
		t.Errorf("mutations = %v\nwant %s", mutations, want)
	}
}

// ---- Discovery per family

// TestAwsFindsRoleByPathNotTaggingAPI — AT1: the tagging API reports the
// instance profile and no role, yet the role is found under /attune/ and
// never recreated; a role outside the path is never pruned.
func TestAwsFindsRoleByPathNotTaggingAPI(t *testing.T) {
	provider, cli := newAwsFake(awsFullAccount(awsSecurityGroupJSON, awsInstanceJSON("ami-0example", "example")))
	changes, err := Plan(provider, awsHostBundle(), &Options{Provider: "aws", PruneHosts: true})
	if err != nil || len(changes) != 0 {
		t.Fatalf("changes=%v err=%v", changeKeys(changes), err)
	}
	pairs := cli.pairs()
	if !slices.Contains(pairs, "iam list-roles") || slices.Contains(pairs, "iam create-role") {
		t.Errorf("calls = %v", pairs)
	}
	for _, call := range cli.calls {
		if call[1] == "list-roles" && cli.argAfter(call, "--path-prefix") != "/attune/" {
			t.Errorf("list-roles argv = %v", call)
		}
	}
}

// TestAwsFindsBucketsInAnyRegion — AT2: buckets come from list-buckets and
// their tags, so a managed bucket in another region is found and planned,
// while an untagged bucket and one whose tags cannot be read are never
// listed or pruned.
func TestAwsFindsBucketsInAnyRegion(t *testing.T) {
	provider, cli := newAwsFake(map[string][]fakeAwsResult{
		"s3api list-buckets":            {{out: `{"Buckets":[{"Name":"example-attune-backups"},{"Name":"plain-bucket"},{"Name":"broken-bucket"}]}`}},
		"s3api get-bucket-tagging":      {{out: awsBucketTagsJSON}, {out: `{"TagSet":[{"Key":"owner","Value":"someone"}]}`}, {exit: 254}},
		"s3api get-bucket-location":     {{out: `{"LocationConstraint":"eu-west-1"}`}},
		"s3api get-public-access-block": {{out: `{"PublicAccessBlockConfiguration":{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}}`}},
		"s3api get-bucket-encryption":   {{out: `{"ServerSideEncryptionConfiguration":{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}}`}},
	})
	provider.region = "us-east-2"
	bundle := &Bundle{Buckets: awsHostBundle().Buckets}
	changes, err := Plan(provider, bundle, &Options{Provider: "aws", PruneHosts: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := changeKeys(changes); strings.Join(got, ";") != "replace bucket|example-attune-backups" || changes[0].Summary != "region is immutable" {
		t.Errorf("changes = %v (%q)", got, changes[0].Summary)
	}
	locations := 0
	for _, call := range cli.calls {
		if call[1] == "get-bucket-location" {
			locations++
		}
	}
	if locations != 1 {
		t.Errorf("get-bucket-location called %d times, want once for the managed bucket only", locations)
	}
}

// TestAwsFindsHealthChecksByListing — AT3: checks come from
// list-health-checks and batched tags; an untagged check is invisible even
// under -M, and twelve ids take two tag calls.
func TestAwsFindsHealthChecksByListing(t *testing.T) {
	provider, _ := newAwsFake(awsFullAccountWithMonitoring(awsBudgetNotifications, awsHealthCheckJSON("HTTPS", 3)))
	grounded(t, provider)
	changes, err := Plan(provider, awsMonitoringBundle(), &Options{Provider: "aws", PruneDNS: true, PruneMonitoring: true})
	if err != nil || len(changes) != 0 {
		t.Fatalf("changes=%v err=%v", changeKeys(changes), err)
	}
	var many strings.Builder
	many.WriteString(`{"HealthChecks":[`)
	for i := 1; i <= 12; i++ {
		if i > 1 {
			many.WriteString(",")
		}
		many.WriteString(`{"Id":"hc-` + strconv.Itoa(i) + `","HealthCheckConfig":{"IPAddress":"203.0.113.1","Port":80,"Type":"HTTP","ResourcePath":"/","RequestInterval":30,"FailureThreshold":3}}`)
	}
	many.WriteString(`]}`)
	provider, cli := newAwsFake(map[string][]fakeAwsResult{
		"route53 list-health-checks":      {{out: many.String()}},
		"route53 list-tags-for-resources": {{out: `{"ResourceTagSets":[]}`}},
	})
	checks, err := provider.ListHealthChecks()
	if err != nil || len(checks) != 0 {
		t.Fatalf("checks=%v err=%v", checks, err)
	}
	tagCalls := 0
	for _, call := range cli.calls {
		if call[1] == "list-tags-for-resources" {
			tagCalls++
			ids := call[slices.Index(call, "--resource-ids")+1 : slices.Index(call, "--output")]
			if len(ids) > 10 || len(ids) < 2 {
				t.Errorf("batch of %d ids: %v", len(ids), call)
			}
		}
	}
	if tagCalls != 2 {
		t.Errorf("list-tags-for-resources called %d times, want 2", tagCalls)
	}
}

// TestAwsAllowlistMatchesDiscovery — AT4: the retired pairs are refused,
// the new ones allowed, and the tagging API is asked only about EC2.
func TestAwsAllowlistMatchesDiscovery(t *testing.T) {
	for _, pair := range []string{"route53 get-health-check", "route53 list-tags-for-resource"} {
		if awsAllowedCommands[pair] {
			t.Errorf("%s still allowlisted", pair)
		}
	}
	for _, pair := range []string{"iam list-roles", "route53 list-health-checks", "route53 list-tags-for-resources", "s3api list-buckets", "s3api get-bucket-tagging"} {
		if !awsAllowedCommands[pair] {
			t.Errorf("%s not allowlisted", pair)
		}
	}
	provider, cli := newAwsFake(map[string][]fakeAwsResult{"resourcegroupstaggingapi get-resources": {{out: awsEmptyTaggingJSON}}})
	if _, err := provider.ListFirewalls(); err != nil {
		t.Fatal(err)
	}
	call := cli.calls[0]
	i := slices.Index(call, "--resource-type-filters")
	if i < 0 || strings.Join(call[i+1:i+5], " ") != "ec2:instance ec2:security-group ec2:elastic-ip --output" {
		t.Errorf("tagging argv = %v", call)
	}
}

// TestAwsLaunchRetriesWhileProfilePropagates — AT5: a launch that fails
// twice succeeds on the third try after two five-second sleeps; one that
// keeps failing is reported after six retries and thirty seconds.
func TestAwsLaunchRetriesWhileProfilePropagates(t *testing.T) {
	newProvider := func(results []fakeAwsResult) (*AwsProvider, *fakeAwsCli, *[]time.Duration) {
		provider, cli := newAwsFake(map[string][]fakeAwsResult{
			"ec2 describe-images": {{out: `{"Images":[{"RootDeviceName":"/dev/xvda"}]}`}},
			"ec2 run-instances":   results,
		})
		var sleeps []time.Duration
		provider.sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
		return provider, cli, &sleeps
	}
	instance := awsHostBundle().Instances[0]
	instance.Image, instance.Firewall, instance.Role = "ami-0example", "", ""
	provider, cli, sleeps := newProvider([]fakeAwsResult{{exit: 254}, {exit: 254}, {out: `{"Instances":[{"InstanceId":"i-0new"}]}`}})
	_ = cli
	id, err := provider.PutInstance(instance)
	if err != nil || id != "i-0new" || len(*sleeps) != 2 || (*sleeps)[0] != 5*time.Second {
		t.Errorf("id=%q err=%v sleeps=%v", id, err, *sleeps)
	}
	provider, cli, sleeps = newProvider([]fakeAwsResult{{exit: 254}})
	_, err = provider.PutInstance(instance)
	launches := 0
	for _, call := range cli.calls {
		if call[1] == "run-instances" {
			launches++
		}
	}
	var total time.Duration
	for _, d := range *sleeps {
		total += d
	}
	if err == nil || !strings.Contains(err.Error(), "aws ec2 run-instances failed with exit 254") || launches != 7 || total != 30*time.Second {
		t.Errorf("err=%v launches=%d slept=%v", err, launches, total)
	}
}

// TestAwsAddressWaitsForTheInstanceToRun — a launched instance is pending
// for a while; the address attaches only once it runs, and an instance
// that stops being pending without running is reported.
func TestAwsAddressWaitsForTheInstanceToRun(t *testing.T) {
	stateJSON := func(state string) string {
		return `{"Reservations":[{"Instances":[{"InstanceId":"i-0new","State":{"Name":"` + state + `"},"Tags":[{"Key":"Name","Value":"example-host"}]}]}]}`
	}
	provider, cli := newAwsFake(map[string][]fakeAwsResult{
		"ec2 describe-instances": {{out: stateJSON("pending")}, {out: stateJSON("pending")}, {out: stateJSON("running")}},
		"ec2 allocate-address":   {{out: `{"AllocationId":"eipalloc-0new","PublicIp":"203.0.113.10"}`}},
		"ec2 associate-address":  {{out: `{}`}},
	})
	var sleeps []time.Duration
	provider.sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	provider.host().instanceIDs["example-host"] = "i-0new"
	ip, err := provider.PutAddress(StaticAddress{Name: "example-host", Instance: "example-host"})
	if err != nil || ip != "203.0.113.10" || len(sleeps) != 2 {
		t.Fatalf("ip=%q err=%v sleeps=%v", ip, err, sleeps)
	}
	pairs := cli.pairs()
	lastPoll := -1
	for i, pair := range pairs {
		if pair == "ec2 describe-instances" {
			lastPoll = i
		}
	}
	if slices.Index(pairs, "ec2 associate-address") < lastPoll {
		t.Errorf("associated before the last poll: %v", pairs)
	}

	provider, cli = newAwsFake(map[string][]fakeAwsResult{"ec2 describe-instances": {{out: stateJSON("pending")}}})
	sleeps = nil
	provider.sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	provider.host().instanceIDs["example-host"] = "i-0new"
	_, err = provider.PutAddress(StaticAddress{Name: "example-host", Instance: "example-host"})
	var total time.Duration
	for _, d := range sleeps {
		total += d
	}
	if err == nil || !strings.Contains(err.Error(), "is pending, not running") || total != 60*time.Second || slices.Contains(cli.pairs(), "ec2 allocate-address") {
		t.Errorf("err=%v slept=%v calls=%v", err, total, cli.pairs())
	}

	provider, _ = newAwsFake(map[string][]fakeAwsResult{"ec2 describe-instances": {{out: stateJSON("terminated")}}})
	provider.sleep = func(time.Duration) {}
	provider.host().instanceIDs["example-host"] = "i-0new"
	if _, err := provider.PutAddress(StaticAddress{Name: "example-host", Instance: "example-host"}); err == nil || !strings.Contains(err.Error(), "is terminated, not running") {
		t.Errorf("terminated instance: %v", err)
	}
}

// alertChainStaysInAlarmRegion asserts that every sns and cloudwatch call
// recorded from index from onward carries --region us-east-1, whatever the
// provider's configured region, since the Route 53 metric, the alarm, and
// the topic it notifies must share that region.
func alertChainStaysInAlarmRegion(t *testing.T, cli *fakeAwsCli, from int) {
	t.Helper()
	seen := 0
	for _, call := range cli.calls[from:] {
		if call[0] != "sns" && call[0] != "cloudwatch" {
			continue
		}
		seen++
		if region := cli.argAfter(call, "--region"); region != "us-east-1" {
			t.Errorf("%s %s ran in %q, want us-east-1: %v", call[0], call[1], region, call)
		}
	}
	if seen == 0 {
		t.Errorf("no sns or cloudwatch call recorded from index %d", from)
	}
}

// TestAwsDocNamesHealthCheckGuardrailEntries: the aws provider doc names the
// four NotAction entries an organization's region guardrail needs for the
// health check's alert chain in us-east-1.
func TestAwsDocNamesHealthCheckGuardrailEntries(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "docs", "aws.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"`cloudwatch:PutMetricAlarm`", "`cloudwatch:DeleteAlarms`", "`cloudwatch:TagResource`", "`sns:*`"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("docs/aws.md lacks %s", want)
		}
	}
}
