package zone

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	klog "k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

var phzName = "cluster-test.local."

type Reconciler struct {
	clientSet    kubernetes.Interface
	corev1Client corev1.CoreV1Interface
	r53          *route53.Route53
	imds         *ec2metadata.EC2Metadata
	sess         session.Session
	phz          *route53.HostedZone
}

func New(clientSet kubernetes.Interface, sess *session.Session) *Reconciler {
	return &Reconciler{
		clientSet:    clientSet,
		corev1Client: clientSet.CoreV1(),
		r53:          route53.New(sess),
		imds:         ec2metadata.New(sess),
		sess:         *sess,
	}
}

func (d *Reconciler) InitPHZ(ctx context.Context) error {
	if d.phz != nil {
		return nil
	}
	region := d.sess.Config.Region
	macsResp, err := d.imds.GetMetadataWithContext(ctx, "/network/interfaces/macs")
	if err != nil {
		return fmt.Errorf("unable to retrieve vpc-id from network interfaces: %v", err)
	}
	macs := strings.Split(macsResp, "\n")
	if len(macs) == 0 {
		return fmt.Errorf("unable to identify primary network interface: %v", err)
	}
	vpcID, err := d.imds.GetMetadataWithContext(ctx, fmt.Sprintf("/network/interfaces/macs/%s/vpc-id", macs[0]))
	if err != nil {
		return fmt.Errorf("unable to retrieve vpc-id from primary network interface: %v", err)
	}
	hzOut, err := d.r53.ListHostedZonesByNameWithContext(ctx, &route53.ListHostedZonesByNameInput{
		DNSName: aws.String(phzName),
	})
	if err != nil {
		klog.Errorf("unable to list route 53 hosted zones: %v", err)
	}
	if len(hzOut.HostedZones) > 0 && *hzOut.HostedZones[0].Name == phzName {
		d.phz = &route53.HostedZone{
			Id:   hzOut.HostedZones[0].Id,
			Name: hzOut.HostedZones[0].Name,
		}
		return nil
	}
	phzOutput, err := d.r53.CreateHostedZoneWithContext(ctx, &route53.CreateHostedZoneInput{
		Name: aws.String(phzName),
		VPC: &route53.VPC{
			VPCRegion: region,
			VPCId:     aws.String(vpcID),
		},
		CallerReference: aws.String(fmt.Sprint(time.Now().UnixNano())),
		HostedZoneConfig: &route53.HostedZoneConfig{
			PrivateZone: aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("unable to create a route 53 private hosted zone: %v", err)
	}
	d.phz = phzOutput.HostedZone
	return nil
}

func (d *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if err := d.InitPHZ(ctx); err != nil {
		klog.Errorf("creating r53 phz: %v", err)
		return ctrl.Result{}, err
	}
	dnsRecords := map[string][]string{}

	// Populate pod A Records
	podList, err := d.corev1Client.Pods(v1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("unable to fetch Pods: %v", err)
	}
	for _, pod := range podList.Items {
		if pod.Status.PodIP == "" {
			continue
		}
		podIP := strings.ReplaceAll(pod.Status.PodIP, ".", "-")
		dnsRecords[fmt.Sprintf("%s.%s.pod.%s", podIP, pod.Namespace, phzName)] = []string{pod.Status.PodIP}
	}

	//Populate Service Records
	svcList, err := d.corev1Client.Services(v1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("unable to fetch Services: %v", err)
	}
	for _, svc := range svcList.Items {
		key := fmt.Sprintf("%s.%s.svc.%s", svc.Name, svc.Namespace, phzName)
		dnsRecords[key] = append(dnsRecords[key], svc.Spec.ClusterIP)
	}

	// Populate PHZ w/ DNS records
	var changeSet []*route53.Change
	for key, vals := range dnsRecords {
		change := &route53.Change{
			Action: aws.String(route53.ChangeActionUpsert),
			ResourceRecordSet: &route53.ResourceRecordSet{
				Name: aws.String(key),
				Type: aws.String("A"),
				TTL:  aws.Int64(60),
			},
		}
		for _, val := range vals {
			change.ResourceRecordSet.ResourceRecords = append(change.ResourceRecordSet.ResourceRecords,
				&route53.ResourceRecord{Value: aws.String(val)})
		}
		changeSet = append(changeSet, change)
	}
	if _, err := d.r53.ChangeResourceRecordSetsWithContext(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: d.phz.Id,
		ChangeBatch: &route53.ChangeBatch{
			Changes: changeSet,
		},
	}); err != nil {
		klog.Errorf("unable to update private hosted zone %s with %d records: %v", *d.phz.Name, len(changeSet), err)
		return ctrl.Result{}, err
	}

	// Delete old records
	var recordsToDelete []string
	if err := d.r53.ListResourceRecordSetsPagesWithContext(ctx, &route53.ListResourceRecordSetsInput{
		HostedZoneId: d.phz.Id,
	}, func(lrrso *route53.ListResourceRecordSetsOutput, _ bool) bool {
		for _, record := range lrrso.ResourceRecordSets {
			if _, ok := dnsRecords[*record.Name]; !ok {
				recordsToDelete = append(recordsToDelete, *record.Name)
			}
		}
		return true
	}); err != nil {
		klog.Errorf("unable to list resource record sets for hosted zone %s: %v", *d.phz.Name, err)
		return ctrl.Result{}, err
	}

	var deleteSet []*route53.Change
	for _, key := range recordsToDelete {
		change := &route53.Change{
			Action: aws.String(route53.ChangeActionDelete),
			ResourceRecordSet: &route53.ResourceRecordSet{
				Name: aws.String(key),
				Type: aws.String("A"),
			},
		}
		deleteSet = append(deleteSet, change)
	}

	if _, err := d.r53.ChangeResourceRecordSetsWithContext(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: d.phz.Id,
		ChangeBatch: &route53.ChangeBatch{
			Changes: changeSet,
		},
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to delete resource record sets for hosted zone %s: %v", *d.phz.Name, err)
	}
	if len(deleteSet) > 0 {
		klog.Infof("Deleted %d DNS resource records that no longer exist in the cluster", len(deleteSet))
	}
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (d *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Pod{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(d)
}
