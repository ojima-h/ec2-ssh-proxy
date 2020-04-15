package main

import (
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"github.com/aws/aws-sdk-go/service/ec2instanceconnect"
	"github.com/aws/aws-sdk-go/service/ec2instanceconnect/ec2instanceconnectiface"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/aws/aws-sdk-go/service/ssm/ssmiface"
	"github.com/jessevdk/go-flags"
	"io/ioutil"
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
	params, err := parseArgs(os.Args[1:])
	if err != nil {
		return err
	}

	client := newClient(params.Profile)

	instanceId, availabilityZone, err := client.findInstance(params)
	if err != nil {
		return err
	}

	err = client.sendPublicKey(params, instanceId, availabilityZone)
	if err != nil {
		return err
	}

	err = client.startSession(params, instanceId)
	if err != nil {
		return err
	}

	return nil
}
s
/*
 * Parse arguments
 */

type Params struct {
	Profile   string
	User      string
	Port      int
	PublicKey string
	// ec2 filter
	Id   string
	Name string
}

func parseArgs(args []string) (*Params, error) {
	ret := Params{}

	var opts struct {
		Pattern string `long:"pattern" description:"Host name pattern" default:"ec2.%(name)"`
		Profile string `long:"profile" description:"Aws credentials profile name"`
		KeyFile string `long:"public-key" description:"SSH public key file path" default:"~/.ssh/id_rsa.pub"`
		User    string `long:"user" description:"OS user on the EC2 instance" default:"ec2-user"`
		Args    struct {
			HOST string
			PORT int
		} `positional-args:"yes" required:"yes"`
	}
	_, err := flags.NewParser(&opts, flags.HelpFlag|flags.PassDoubleDash).ParseArgs(args)
	if err != nil {
		return nil, err
	}

	ret.Profile = opts.Profile
	ret.User = opts.User
	ret.Port = opts.Args.PORT

	// read SSH public key
	kf := opts.KeyFile
	if strings.HasPrefix(kf, "~/") {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		kf = filepath.Join(h, kf[2:])
	}
	k, err := ioutil.ReadFile(kf)
	if err != nil {
		return nil, err
	}
	ret.PublicKey = string(k)

	err = parseHostname(opts.Args.HOST, opts.Pattern, &ret)
	if err != nil {
		return nil, err
	}

	return &ret, nil
}

func parseHostname(hostname string, pattern string, p *Params) error {
	pat := pattern
	pat = strings.ReplaceAll(pat, "{name}", `(?P<name>[\w-]+)`)
	pat = strings.ReplaceAll(pat, "{id}", `(?P<id>[\w-]+)`)
	pat = strings.ReplaceAll(pat, "{profile}", `(?P<profile>[\w-]+)`)

	re, err := regexp.Compile(pat)
	if err != nil {
		return fmt.Errorf("invalid host name pattern: %s", pattern)
	}

	keys := re.SubexpNames()
	vals := re.FindStringSubmatch(hostname)
	for i, k := range keys {
		v := vals[i]
		if k == "name" {
			p.Name = v
		}
		if k == "id" {
			p.Id = v
		}
		if k == "profile" {
			p.Profile = v
		}
	}

	if p.Name != "" && p.Id != "" {
		return fmt.Errorf("name and id could not be specified at same time")
	}
	if p.Name == "" && p.Id == "" {
		return fmt.Errorf("neither name nor id is specified")
	}

	return nil
}

/*
 * AWS client
 */

type Client struct {
	ec2   ec2iface.EC2API
	ec2ic ec2instanceconnectiface.EC2InstanceConnectAPI
	ssm   ssmiface.SSMAPI

	ssmSigningRegion string
	ssmEndpoint      string
	plugin SessionManagerPlugin
}

func newClient(profile string) *Client {
	c := Client{}

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Profile:           profile,
		SharedConfigState: session.SharedConfigEnable,
	}))
	c.ec2 = ec2.New(sess)
	c.ec2ic = ec2instanceconnect.New(sess)

	s := ssm.New(sess)
	c.ssm = s
	c.ssmSigningRegion = s.SigningRegion
	c.ssmEndpoint = s.Endpoint

	c.plugin = newSessionManagerPlugin()

	return &c
}

func (c *Client) findInstance(params *Params) (instanceId string, availabilityZone string, err error) {
	in := ec2.DescribeInstancesInput{}
	if params.Name != "" {
		in.Filters = []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(params.Name)},
			},
		}
	}
	if params.Id != "" {
		in.InstanceIds = []*string{
			aws.String(params.Id),
		}
	}
	out, err := c.ec2.DescribeInstances(&in)
	if err != nil {
		return
	}
	if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
		err = fmt.Errorf("ec2 instance is not found")
		return
	}

	instanceId = aws.StringValue(out.Reservations[0].Instances[0].InstanceId)
	availabilityZone = aws.StringValue(out.Reservations[0].Instances[0].Placement.AvailabilityZone)
	return
}

func (c *Client) sendPublicKey(params *Params, instanceId string, availabilityZone string) error {
	in := ec2instanceconnect.SendSSHPublicKeyInput{
		AvailabilityZone: aws.String(availabilityZone),
		InstanceId:       aws.String(instanceId),
		InstanceOSUser:   aws.String(params.User),
		SSHPublicKey:     aws.String(params.PublicKey),
	}
	_, err := c.ec2ic.SendSSHPublicKey(&in)
	if err != nil {
		return err
	}

	return nil
}

func (c *Client) startSession(params *Params, instanceId string) (err error) {
	err = c.plugin.check()
	if err != nil {
		return err
	}

	in := &ssm.StartSessionInput{
		Target:       aws.String(instanceId),
		DocumentName: aws.String("AWS-StartSSHSession"),
		Parameters: map[string][]*string{
			"portNumber": {aws.String(strconv.Itoa(params.Port))},
		},
	}
	out, err := c.ssm.StartSession(in)
	if err != nil {
		return
	}

	err = c.plugin.start(params, c.ssmSigningRegion, c.ssmEndpoint, in, out)
	if err != nil {
		return err
	}

	return
}

/*
 * session-manager-plugin
 */

type SessionManagerPlugin interface {
	check() error
	start(params *Params, region string, endpoint string, ssmInput *ssm.StartSessionInput, ssmOutput *ssm.StartSessionOutput) error
}

type SessionManagerPluginImpl struct {}

func newSessionManagerPlugin() SessionManagerPlugin {
	return &SessionManagerPluginImpl{}
}

func (*SessionManagerPluginImpl) check() error {
	_, err := exec.LookPath("session-manager-plugin")
	if err != nil {
		return fmt.Errorf("SessionManagerPlugin is not found. \n" +
			"Please refer to SessionManager Documentation here: \n" +
			"http://docs.aws.amazon.com/console/systems-manager/\n" +
			"session-manager-plugin-not-found")
	}
	return nil
}

func (c *SessionManagerPluginImpl) start(params *Params, region string, endpoint string, in *ssm.StartSessionInput, out *ssm.StartSessionOutput) error {
	i, err := json.Marshal(in)
	if err != nil {
		return err
	}
	o, err := json.Marshal(out)
	if err != nil {
		return err
	}

	cmd := exec.Command(
		"session-manager-plugin",
		string(o),
		region,
		"StartSession",
		params.Profile,
		string(i),
		endpoint,
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	c.ignoreUserSignals(func() {
		err = cmd.Run()
	})
	if err != nil {
		return err
	}

	return nil
}

func (*SessionManagerPluginImpl) ignoreUserSignals(f func()) {
	var sig []os.Signal
	if runtime.GOOS == "windows" {
		sig = []os.Signal{syscall.SIGINT}
	} else {
		sig = []os.Signal{syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP}
	}

	signal.Ignore(sig...)
	defer signal.Reset(sig...)

	f()
}
