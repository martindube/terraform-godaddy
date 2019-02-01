package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform/helper/schema"
	"github.com/martindube/terraform-godaddy/api"
)

type domainRecordResource struct {
	Customer  string
	Domain    string
	Records   []*api.DomainRecord
	ARecords  []string
	NSRecords []string
}

// Warning: This array cannot be empty. Seems to be a godaddy limitation.
var defaultRecords = []*api.DomainRecord{
	{Type: api.TXTType, Name: "@", Data: "Domain Deleted :)", TTL: api.DefaultTTL, Priority: 0},
}

func newDomainRecordResource(d *schema.ResourceData) (domainRecordResource, error) {
	var err error
	r := domainRecordResource{}

	if attr, ok := d.GetOk("customer"); ok {
		r.Customer = attr.(string)
	}

	if attr, ok := d.GetOk("domain"); ok {
		r.Domain = attr.(string)
	}

	if attr, ok := d.GetOk("record"); ok {
		records := attr.(*schema.Set).List()
		r.Records = make([]*api.DomainRecord, len(records))

		for i, rec := range records {
			data := rec.(map[string]interface{})
			r.Records[i], err = api.NewDomainRecord(
				data["name"].(string),
				data["type"].(string),
				data["data"].(string),
				data["ttl"].(int),
				data["priority"].(int))

			if err != nil {
				return r, err
			}
		}
	}

	if attr, ok := d.GetOk("nameservers"); ok {
		records := attr.([]interface{})
		r.NSRecords = make([]string, len(records))
		for i, rec := range records {
			if err = api.ValidateData(api.NSType, rec.(string)); err != nil {
				return r, err
			}
			r.NSRecords[i] = rec.(string)
		}
	}

	if attr, ok := d.GetOk("addresses"); ok {
		records := attr.([]interface{})
		r.ARecords = make([]string, len(records))
		for i, rec := range records {
			if err = api.ValidateData(api.AType, rec.(string)); err != nil {
				return r, err
			}
			r.ARecords[i] = rec.(string)
		}
	}

	return r, err
}

func (r *domainRecordResource) converge() {
	r.mergeRecords(r.ARecords, api.NewARecord)
	r.mergeRecords(r.NSRecords, api.NewNSRecord)
}

func (r *domainRecordResource) mergeRecords(list []string, factory api.RecordFactory) {
	for _, data := range list {
		record, _ := factory(data)
		r.Records = append(r.Records, record)
	}
}

func resourceDomainRecord() *schema.Resource {
	return &schema.Resource{
		Create: resourceDomainRecordUpdate,
		Read:   resourceDomainRecordRead,
		Update: resourceDomainRecordUpdate,
		Delete: resourceDomainRecordRestore,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			// Required
			"domain": {
				Type:     schema.TypeString,
				Required: true,
			},
			// Optional
			"addresses": {
				Type:     schema.TypeList,
				Optional: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
			"customer": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"nameservers": {
				Type:     schema.TypeList,
				Optional: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
			"record": {
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Required: true,
						},
						"type": {
							Type:     schema.TypeString,
							Required: true,
						},
						"data": {
							Type:     schema.TypeString,
							Required: true,
						},
						"ttl": {
							Type:     schema.TypeInt,
							Optional: true,
							Default:  api.DefaultTTL,
						},
						"priority": {
							Type:     schema.TypeInt,
							Optional: true,
							Default:  api.DefaultPriority,
						},
					},
				},
			},
		},
	}
}

func resourceDomainRecordRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*api.Client)
	customer := d.Get("customer").(string)
	domain := d.Get("domain").(string)

	// Importer support
	if domain == "" {
		domain = d.Id()
	}

	log.Println("Fetching", domain, "records...")
	records, err := client.GetDomainRecords(customer, domain)
	if err != nil {
		return fmt.Errorf("couldn't find domain record (%s): %s", domain, err.Error())
	}

	return populateResourceDataFromResponse(records, d)
}

func resourceDomainRecordUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*api.Client)

	// If no nameservers are set, get current NS records.
	if _, ok := d.GetOk("nameservers"); !ok {
		customer := d.Get("customer").(string)
		domain := d.Get("domain").(string)

		log.Println("Fetching", domain, "records...")
		records, err := client.GetDomainRecords(customer, domain)
		if err != nil {
			return fmt.Errorf("couldn't find domain record (%s): %s", domain, err.Error())
		}

		err = populateNSFromResponse(records, d)
	}

	r, err := newDomainRecordResource(d)
	if err != nil {
		return err
	}

	if err = populateDomainInfo(client, &r, d); err != nil {
		return err
	}

	log.Println("Updating", r.Domain, "domain records...")
	r.converge()
	err = client.UpdateDomainRecords(r.Customer, r.Domain, r.Records)

	// Ignore name server validation errors
	if err != nil && strings.Contains(err.Error(), "422:FAILED_NAME_SERVER_VALIDATION") {
		log.Printf("Warning: Nameserver were not changed: %s", err.Error())
		return nil
	}
	return err
}

func resourceDomainRecordRestore(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*api.Client)
	r, err := newDomainRecordResource(d)
	if err != nil {
		return err
	}

	if err = populateDomainInfo(client, &r, d); err != nil {
		return err
	}

	r.Records = defaultRecords
	r.mergeRecords(r.NSRecords, api.NewNSRecord)

	log.Println("Restoring", r.Domain, "domain records...")
	err = client.UpdateDomainRecords(r.Customer, r.Domain, r.Records)

	// Ignore name server validation errors
	if err != nil && strings.Contains(err.Error(), "422:FAILED_NAME_SERVER_VALIDATION") {
		log.Printf("Warning: Nameserver were not changed: %s", err.Error())
		return nil
	}
	return err
}

func populateDomainInfo(client *api.Client, r *domainRecordResource, d *schema.ResourceData) error {
	var err error
	var domain *api.Domain

	log.Println("Fetching", r.Domain, "info...")
	domain, err = client.GetDomain(r.Customer, r.Domain)
	if err != nil {
		return fmt.Errorf("couldn't find domain (%s): %s", r.Domain, err.Error())
	}

	d.SetId(strconv.FormatInt(domain.ID, 10))
	return nil
}

func populateNSFromResponse(r []*api.DomainRecord, d *schema.ResourceData) error {
	nsRecords := make([]string, 0)

	for _, rec := range r {
		if api.IsDefaultNSRecord(rec) {
			nsRecords = append(nsRecords, rec.Data)
		}
	}

	d.Set("nameservers", nsRecords)

	return nil
}

func populateResourceDataFromResponse(r []*api.DomainRecord, d *schema.ResourceData) error {
	aRecords := make([]string, 0)
	nsRecords := make([]string, 0)
	records := make([]*api.DomainRecord, 0)

	for _, rec := range r {
		switch {
		case api.IsDefaultNSRecord(rec):
			nsRecords = append(nsRecords, rec.Data)
		case api.IsDefaultARecord(rec):
			aRecords = append(aRecords, rec.Data)
		default:
			records = append(records, rec)
		}
	}

	d.Set("addresses", aRecords)
	d.Set("nameservers", nsRecords)
	d.Set("record", flattenRecords(records))

	return nil
}

func flattenRecords(list []*api.DomainRecord) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(list))
	for _, r := range list {
		l := map[string]interface{}{
			"name":     r.Name,
			"type":     r.Type,
			"data":     r.Data,
			"ttl":      r.TTL,
			"priority": r.Priority,
		}
		result = append(result, l)
	}
	return result
}
