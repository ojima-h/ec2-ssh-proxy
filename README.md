# ec2-ssh-proxy

`ec2-ssh-proxy` can be used in SSH ProxyCommand, and enables you to connect EC2 instance without managing ssh keys or
opening ssh server port.

## Getting Started

1. First, set up your AWS account that you can use _SSH over Session Manger_ and _EC2 Instance Connect_.
    1. Confirm that an instance profile that contains the AWS managed policy `AmazonSSMManagedInstanceCore` is attached to
        your target instances.
    
    1. Confirm you have right permissions to access
        [EC2 Instance Connect](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-connect-set-up.html#ec2-instance-connect-configure-IAM-role)
        and [Session Manager](https://docs.aws.amazon.com/systems-manager/latest/userguide/getting-started-restrict-access-quickstart.html).
        
        IAM Policy example:
        
        ```json
        {
            "Version": "2012-10-17",
            "Statement": [
                {
                    "Sid": "AllowSendSSHPublicKey",
                    "Effect": "Allow",
                    "Action": "ec2-instance-connect:SendSSHPublicKey",
                    "Resource": "arn:aws:ec2:*:*:instance/*",
                    "Condition": {
                        "StringEquals": {
                            "ec2:osuser": "ec2-user"
                        }
                    }
                },
                {
                    "Sid": "AllowStartSession",
                    "Effect": "Allow",
                    "Action": "ssm:StartSession",
                    "Resource": [
                        "arn:aws:ec2:*:*:instance/*",
                        "arn:aws:ssm:*:*:document/AWS-StartSSHSession"
                    ]
                },
                {
                    "Sid": "AllowDescribeSessions",
                    "Effect": "Allow",
                    "Action": [
                        "ssm:GetConnectionStatus",
                        "ssm:DescribeSessions",
                        "ssm:DescribeInstanceProperties",
                        "ec2:DescribeInstances"
                    ],
                    "Resource": "*"
                },
                {
                    "Sid": "AllowTerminateYourSession",
                    "Effect": "Allow",
                    "Action": "ssm:TerminateSession",
                    "Resource": "arn:aws:ssm:*:*:session/${aws:username}-*"
                }
            ]
        }
        ```  
        
2. Install `ec2-instance-connect`.
 
    Download the binary from https://github.com/ojima-h/ec2-ssh-proxy/releases.

3. Install the Session Manager Plugin.

    See https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html 

3. Get AWS access key and secret key, and configure credentials.

    ```console
    $ aws configure [--profile ...]
    ```
   
4. Configure your `~/.ssh/config` file:

    ```
    Host ec2.*
        User ec2-user
        ProxyCommand ec2-ssh-proxy %h %p
    ```

Now, you can connect to an EC2 instance as follows:

```
ssh ec2.YOUR_INSTANCE_NAME
```
