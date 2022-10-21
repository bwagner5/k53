package zone

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	v1 "k8s.io/api/core/v1"
	klog "k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var phzName = "cluster-test.local."

type Reconciler struct {
	client client.Client
	r53    *route53.Route53
	imds   *ec2metadata.EC2Metadata
	sess   session.Session
	phz    *route53.HostedZone
}

func New(client client.Client, sess *session.Session) *Reconciler {
	return &Reconciler{
		client: client,
		r53:    route53.New(sess),
		imds:   ec2metadata.New(sess),
		sess:   *sess,
	}
}

// SetupWithManager sets up the controller with the Manager.
func (d *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("zone").
		For(&v1.Pod{}).
		Watches(&source.Kind{Type: &v1.Service{}}, &handler.EnqueueRequestForObject{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(d)
}

func (d *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if err := d.CreatePrivateHostedZone(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("creating Route 53 private hosted zone: %w", err)
	}

	podRecords, err := d.GeneratePodARecords(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("generating pod A records, %w", err)
	}
	klog.V(10).Infof("Pod Records: %v", d.prettyPrintRecordSets(podRecords))

	serviceRecords, err := d.GenerateServiceARecords(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("generating service A records, %w", err)
	}
	klog.V(10).Infof("Service Records: %v", d.prettyPrintRecordSets(serviceRecords))

	existingRecords, err := d.ListResourceRecords(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing existing records from Route 53 private hosted zone, %w", err)
	}
	klog.V(5).Infof("Found %d existing records", len(existingRecords))

	updated, err := d.UpsertRecords(ctx, existingRecords, podRecords, serviceRecords)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("upserting private hosted zone records, %w", err)
	}
	klog.V(5).Infof("Upserted %d records", updated)

	deletedRecords, err := d.DeleteOldRecords(ctx, existingRecords, podRecords, serviceRecords)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("delete old records from private hosted zone, %w", err)
	}
	if len(deletedRecords) > 0 {
		klog.Infof("Deleted %d DNS resource record(s) that no longer exist in the cluster", len(deletedRecords))
		klog.V(10).Infof("Deleted Records: %v", d.prettyPrintRecordSets(deletedRecords))
	}

	jitter := time.Duration(rand.Int63n(int64(2 * time.Minute)))
	return ctrl.Result{RequeueAfter: (5 * time.Minute) + jitter}, nil
}

func (d *Reconciler) GeneratePodARecords(ctx context.Context) (map[string]*route53.ResourceRecordSet, error) {
	dnsRecords := map[string]*route53.ResourceRecordSet{}
	var podList v1.PodList
	if err := d.client.List(ctx, &podList); err != nil {
		return nil, fmt.Errorf("unable to fetch Pods: %w", err)
	}
	for _, pod := range podList.Items {
		if pod.Status.PodIP == "" {
			continue
		}
		for _, podIP := range pod.Status.PodIPs {
			ip := net.ParseIP(podIP.IP)
			if ip == nil {
				klog.Errorf("invalid IP address for pod %s/%s: %s", pod.Namespace, pod.Name, podIP.IP)
				continue
			}
			recordType := "A"
			if isIpv4 := ip.To4(); isIpv4 == nil {
				recordType = "AAAA"
			}

			podIPHostname := strings.ReplaceAll(podIP.IP, ".", "-")
			key := fmt.Sprintf("%s.%s.pod.%s", podIPHostname, pod.Namespace, phzName)
			dnsRecords[key] = &route53.ResourceRecordSet{
				Name: &key,
				Type: aws.String(recordType),
				TTL:  aws.Int64(60),
				ResourceRecords: []*route53.ResourceRecord{
					{
						Value: aws.String(podIP.IP),
					},
				},
			}
		}
	}
	return dnsRecords, nil
}

func (d *Reconciler) GenerateServiceARecords(ctx context.Context) (map[string]*route53.ResourceRecordSet, error) {
	dnsRecords := map[string]*route53.ResourceRecordSet{}
	var svcList v1.ServiceList
	if err := d.client.List(ctx, &svcList); err != nil {
		return nil, fmt.Errorf("unable to fetch Services: %w", err)
	}
	for _, svc := range svcList.Items {
		clusterIP := svc.Spec.ClusterIP
		key := fmt.Sprintf("%s.%s.svc.%s", svc.Name, svc.Namespace, phzName)
		dnsRecords[key] = &route53.ResourceRecordSet{
			Name: &key,
			Type: aws.String("A"),
			TTL:  aws.Int64(60),
			ResourceRecords: []*route53.ResourceRecord{
				{
					Value: aws.String(clusterIP),
				},
			},
		}
	}
	return dnsRecords, nil
}

func (d *Reconciler) UpsertRecords(ctx context.Context, existingRecords map[string]*route53.ResourceRecordSet, recordSets ...map[string]*route53.ResourceRecordSet) (int, error) {
	var changeSet []*route53.Change
	for _, records := range recordSets {
		for _, recordSet := range records {
			rs := recordSet
			if existingRecord, ok := existingRecords[*rs.Name]; ok && d.IsRecordSetEqual(existingRecord, rs) {
				continue
			}
			changeSet = append(changeSet, &route53.Change{
				Action:            aws.String(route53.ChangeActionUpsert),
				ResourceRecordSet: rs,
			})
		}
	}
	if len(changeSet) == 0 {
		return 0, nil
	}
	if _, err := d.r53.ChangeResourceRecordSetsWithContext(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: d.phz.Id,
		ChangeBatch: &route53.ChangeBatch{
			Changes: changeSet,
		},
	}); err != nil {
		return 0, fmt.Errorf("unable to update private hosted zone %s with %d records: %w", *d.phz.Name, len(changeSet), err)
	}
	return len(changeSet), nil
}

func (d *Reconciler) IsRecordSetEqual(rsa *route53.ResourceRecordSet, rsb *route53.ResourceRecordSet) bool {
	if *rsa.Type != *rsb.Type || *rsa.TTL != *rsb.TTL || len(rsa.ResourceRecords) != len(rsb.ResourceRecords) {
		return false
	}
	ra := rsa.ResourceRecords
	sort.Slice(ra, func(i, j int) bool {
		return *ra[i].Value < *ra[j].Value
	})
	rb := rsb.ResourceRecords
	sort.Slice(rb, func(i, j int) bool {
		return *rb[i].Value < *rb[j].Value
	})
	for i, r := range ra {
		if *r.Value != *rb[i].Value {
			return false
		}
	}
	return true
}

func (d *Reconciler) DeleteOldRecords(ctx context.Context, existingRecords map[string]*route53.ResourceRecordSet, recordMaps ...map[string]*route53.ResourceRecordSet) (map[string]*route53.ResourceRecordSet, error) {
	recordsToDelete := existingRecords
	for existingRecord := range existingRecords {
		for _, recordSets := range recordMaps {
			if _, ok := recordSets[existingRecord]; ok {
				delete(recordsToDelete, existingRecord)
				break
			}
		}
	}

	var deleteSet []*route53.Change
	for _, recordSet := range recordsToDelete {
		rs := recordSet
		change := &route53.Change{
			Action:            aws.String(route53.ChangeActionDelete),
			ResourceRecordSet: rs,
		}
		deleteSet = append(deleteSet, change)
	}
	if len(deleteSet) > 0 {
		if _, err := d.r53.ChangeResourceRecordSetsWithContext(ctx, &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: d.phz.Id,
			ChangeBatch: &route53.ChangeBatch{
				Changes: deleteSet,
			},
		}); err != nil {
			return nil, fmt.Errorf("unable to delete resource record sets for hosted zone %s: %v", *d.phz.Name, err)
		}
	}
	return recordsToDelete, nil
}

func (d *Reconciler) ListResourceRecords(ctx context.Context) (map[string]*route53.ResourceRecordSet, error) {
	existingRecords := map[string]*route53.ResourceRecordSet{}
	if err := d.r53.ListResourceRecordSetsPagesWithContext(ctx, &route53.ListResourceRecordSetsInput{
		HostedZoneId: d.phz.Id,
	}, func(lrrso *route53.ListResourceRecordSetsOutput, _ bool) bool {
		for _, recordSet := range lrrso.ResourceRecordSets {
			r := recordSet
			if *r.Type == "A" || *r.Type == "AAAA" {
				existingRecords[*r.Name] = r
			}
		}
		return true
	}); err != nil {
		return nil, fmt.Errorf("unable to list resource record sets for hosted zone %s: %w", *d.phz.Name, err)
	}
	return existingRecords, nil
}

func (d *Reconciler) CreatePrivateHostedZone(ctx context.Context) error {
	if d.phz != nil {
		return nil
	}
	vpcID, err := d.getVPCID(ctx)
	if err != nil {
		return fmt.Errorf("unable to get vpc id %w", err)
	}
	hzOut, err := d.r53.ListHostedZonesByNameWithContext(ctx, &route53.ListHostedZonesByNameInput{
		DNSName: aws.String(phzName),
	})
	if err != nil {
		return fmt.Errorf("unable to list route 53 hosted zones: %v", err)
	}
	if len(hzOut.HostedZones) > 0 && *hzOut.HostedZones[0].Name == phzName {
		d.phz = hzOut.HostedZones[0]
		return nil
	}
	phzOutput, err := d.r53.CreateHostedZoneWithContext(ctx, &route53.CreateHostedZoneInput{
		Name: aws.String(phzName),
		VPC: &route53.VPC{
			VPCRegion: d.sess.Config.Region,
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

func (d *Reconciler) getVPCID(ctx context.Context) (string, error) {
	macsResp, err := d.imds.GetMetadataWithContext(ctx, "/network/interfaces/macs")
	if err != nil {
		return "", fmt.Errorf("unable to retrieve vpc-id from network interfaces: %v", err)
	}
	macs := strings.Split(macsResp, "\n")
	if len(macs) == 0 {
		return "", fmt.Errorf("unable to identify primary network interface: %v", err)
	}
	vpcID, err := d.imds.GetMetadataWithContext(ctx, fmt.Sprintf("/network/interfaces/macs/%s/vpc-id", macs[0]))
	if err != nil {
		return "", fmt.Errorf("unable to retrieve vpc-id from primary network interface: %v", err)
	}
	return vpcID, nil
}

func (d *Reconciler) prettyPrintRecordSets(recordSets map[string]*route53.ResourceRecordSet) string {
	var recordSetStrs []string
	for _, rs := range recordSets {
		recordSetStrs = append(recordSetStrs, fmt.Sprintf("%s -> %s", *rs.Name, *rs.ResourceRecords[0].Value))
	}
	return strings.Join(recordSetStrs, ", ")
}
