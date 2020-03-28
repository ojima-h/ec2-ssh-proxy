package main

import (
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2instanceconnect"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/jessevdk/go-flags"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	err := run()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run() error {
	var opts struct {
		Pattern string `long:"pattern" description:"Host name pattern" default:"ec2:%(name)"`
		Profile string `long:"profile" description:"Aws credentials profile name"`
		KeyFile string `long:"public-key" description:"SSH public key file path"`
		User    string `long:"user" description:"OS user on the EC2 instance"`
		Args    struct {
			HOST    string
			PORT    int
		} `positional-args:"yes" required:"yes"`
	}
	_, err := flags.Parse(&opts)
	if err != nil {
		return fmt.Errorf("")
	}
	if opts.KeyFile == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		opts.KeyFile = filepath.Join(h, ".ssh/id_rsa.pub")
	}
	if opts.User == "" {
		opts.User = "ec2-user"
	}
	attrs, err := parseHost(opts.Args.HOST, opts.Pattern)
	if err != nil {
		return err
	}
	if attrs.Profile == "" {
		attrs.Profile = opts.Profile
	}

	publicKey, err := ioutil.ReadFile(opts.KeyFile)
	if err != nil {
		return err
	}

	cli := newAwscli(attrs.Profile)

	in1 := ec2.DescribeInstancesInput{}
	if attrs.Name != "" {
		in1.Filters = []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(attrs.Name)},
			},
		}
	}
	if attrs.Id != "" {
		in1.InstanceIds = []*string{
			aws.String(attrs.Id),
		}
	}
	out1, err := cli.DescribeInstances(&in1)
	if err != nil {
		return err
	}
	if len(out1.Reservations) == 0 || len(out1.Reservations[0].Instances) == 0 {
		return fmt.Errorf("ec2 instance is not found")
	}

	instanceId := aws.StringValue(out1.Reservations[0].Instances[0].InstanceId)
	availabilityZone := aws.StringValue(out1.Reservations[0].Instances[0].Placement.AvailabilityZone)

	in2 := ec2instanceconnect.SendSSHPublicKeyInput{
		AvailabilityZone: aws.String(availabilityZone),
		InstanceId:       aws.String(instanceId),
		InstanceOSUser: aws.String(opts.User),
		SSHPublicKey: aws.String(string(publicKey)),
	}
	_, err = cli.ec2ic.SendSSHPublicKey(&in2)
	if err != nil {
		return err
	}

	in3 := ssm.StartSessionInput{
		Target:       aws.String(instanceId),
		DocumentName: aws.String("AWS-StartSSHSession"),
		Parameters:   map[string][]*string{
			"portNumber": { aws.String(strconv.Itoa(opts.Args.PORT)) },
		},
	}
	out3, err := cli.StartSession(&in3)
	if err != nil {
		return err
	}

	j1, err := json.Marshal(in3)
	if err != nil {
		return err
	}
	j2, err := json.Marshal(out3)
	if err != nil {
		return err
	}
	cmd := exec.Command(
		"session-manager-plugin",
		string(j2),
		cli.ssm.SigningRegion,
		"StartSession",
		attrs.Profile,
		string(j1),
		cli.ssm.Endpoint,
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if cmd.Path == "" {
		_, err1 := cli.ssm.TerminateSession(&ssm.TerminateSessionInput{SessionId: out3.SessionId})
		if err1 != nil {
			log.Printf("[WARN] %s", err1.Error())
		}
		return fmt.Errorf("SessionManagerPlugin is not found. \n" +
			"Please refer to SessionManager Documentation here: \n" +
			"http://docs.aws.amazon.com/console/systems-manager/\n" +
			"session-manager-plugin-not-found")
	}

	ignoreUserSignals(func() {
		err = cmd.Run()
	})
	if err != nil {
		return err
	}

	return nil
}

type HostAttributes struct {
	Name string
	Id string
	Profile string
}

func parseHost(hostname string, pattern string) (*HostAttributes, error) {
	pat := pattern
	pat = strings.ReplaceAll(pat, "{name}", `(?P<name>[\w-]+)`)
	pat = strings.ReplaceAll(pat, "{id}", `(?P<id>[\w-]+)`)
	pat = strings.ReplaceAll(pat, "{profile}", `(?P<profile>[\w-]+)`)

	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("invalid host name pattern: %s", pattern)
	}

	keys := re.SubexpNames()
	vals := re.FindStringSubmatch(hostname)
	attrs := HostAttributes{}
	for i, k := range keys {
		v := vals[i]
		if k == "name" {
			attrs.Name = v
		}
		if k == "id" {
			attrs.Id = v
		}
		if k == "profile" {
			attrs.Profile = v
		}
	}

	if attrs.Name != "" && attrs.Id != "" {
		return nil, fmt.Errorf("name and id could not be specified at same time")
	}
	if attrs.Name == "" && attrs.Id == "" {
		return nil, fmt.Errorf("neither name nor id is specified")
	}

	return &attrs, nil
}

type awscli struct {
	sess *session.Session
	ec2 *ec2.EC2
	ec2ic *ec2instanceconnect.EC2InstanceConnect
	ssm *ssm.SSM
}

func newAwscli(profile string) *awscli {
	c := awscli{}

	c.sess = session.Must(session.NewSessionWithOptions(session.Options{
		Profile: profile,
		SharedConfigState: session.SharedConfigEnable,
	}))
	c.ec2 = ec2.New(c.sess)
	c.ec2ic = ec2instanceconnect.New(c.sess)
	c.ssm = ssm.New(c.sess)

	return &c
}

func (c *awscli) DescribeInstances(input *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
	return c.ec2.DescribeInstances(input)
}

func (c *awscli) StartSession(input *ssm.StartSessionInput) (*ssm.StartSessionOutput, error) {
	return c.ssm.StartSession(input)
}

func ignoreUserSignals(f func()) {
	var sig []os.Signal
	if runtime.GOOS == "windows" {
		sig = []os.Signal{ syscall.SIGINT }
	} else {
		sig = []os.Signal{ syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP }
	}

	signal.Ignore(sig...)
	defer signal.Reset(sig...)

	f()
}