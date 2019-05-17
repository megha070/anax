package agreementbot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang/glog"
	"github.com/open-horizon/anax/businesspolicy"
	"github.com/open-horizon/anax/events"
	"github.com/open-horizon/anax/exchange"
	"github.com/open-horizon/anax/externalpolicy"
	"github.com/open-horizon/anax/policy"
	"golang.org/x/crypto/sha3"
	"sync"
	"time"
)

type ServicePolicyEntry struct {
	Policy  *policy.Policy `json:"policy,omitempty"`      // the metadata for this service policy from the exchange, it is the converted to the internal policy format
	Updated uint64         `json:"updatedTime,omitempty"` // the time when this entry was updated
	Hash    []byte         `json:"hash,omitempty"`        // a hash of the service policy to compare for matadata changes in the exchange
}

func (p *ServicePolicyEntry) String() string {
	return fmt.Sprintf("ServicePolicyEntry: "+
		"Updated: %v "+
		"Hash: %x "+
		"Policy: %v",
		p.Updated, p.Hash, p.Policy)
}

func (p *ServicePolicyEntry) ShortString() string {
	return fmt.Sprintf("ServicePolicyEntry: "+
		"Updated: %v "+
		"Hash: %x "+
		"Policy: %v",
		p.Updated, p.Hash, p.Policy.Header.Name)
}

func NewServicePolicyEntry(p *externalpolicy.ExternalPolicy, svcId string) (*ServicePolicyEntry, error) {
	pSE := new(ServicePolicyEntry)
	pSE.Updated = uint64(time.Now().Unix())
	if hash, err := hashPolicy(p); err != nil {
		return nil, err
	} else {
		pSE.Hash = hash
	}

	if pPolicy, err := policy.GenPolicyFromExternalPolicy(p, svcId); err != nil {
		return nil, err
	} else {
		pSE.Policy = pPolicy
	}
	return pSE, nil
}

type BusinessPolicyEntry struct {
	Policy          *policy.Policy                 `json:"policy,omitempty"`          // the metadata for this business policy from the exchange, it is the converted to the internal policy format
	Updated         uint64                         `json:"updatedTime,omitempty"`     // the time when this entry was updated
	Hash            []byte                         `json:"hash,omitempty"`            // a hash of the business policy to compare for matadata changes in the exchange
	ServicePolicies map[string]*ServicePolicyEntry `json:"servicePolicies,omitempty"` // map of the service id and service policies
}

func NewBusinessPolicyEntry(pol *businesspolicy.BusinessPolicy, polId string) (*BusinessPolicyEntry, error) {
	pBE := new(BusinessPolicyEntry)
	pBE.Updated = uint64(time.Now().Unix())
	if hash, err := hashPolicy(pol); err != nil {
		return nil, err
	} else {
		pBE.Hash = hash
	}
	pBE.ServicePolicies = make(map[string]*ServicePolicyEntry, 0)

	// validate and convert the exchange business policy to internal policy format
	if err := pol.Validate(); err != nil {
		return nil, fmt.Errorf("Failed to validate the business policy %v. %v", *pol, err)
	} else if pPolicy, err := pol.GenPolicyFromBusinessPolicy(polId); err != nil {
		return nil, fmt.Errorf("Failed to convert the business policy to internal policy format: %v. %v", *pol, err)
	} else {
		pBE.Policy = pPolicy
	}

	return pBE, nil
}

func (p *BusinessPolicyEntry) String() string {
	return fmt.Sprintf("BusinessPolicyEntry: "+
		"Updated: %v "+
		"Hash: %x "+
		"Policy: %v"+
		"ServicePolicies: %v",
		p.Updated, p.Hash, p.Policy, p.ServicePolicies)
}

func (p *BusinessPolicyEntry) ShortString() string {
	keys := make([]string, 0, len(p.ServicePolicies))
	for k, _ := range p.ServicePolicies {
		keys = append(keys, k)
	}
	return fmt.Sprintf("BusinessPolicyEntry: "+
		"Updated: %v "+
		"Hash: %x "+
		"Policy: %v"+
		"ServicePolicies: %v",
		p.Updated, p.Hash, p.Policy.Header.Name, keys)
}

// hash the business policy or the service policy gotten from the exchange
func hashPolicy(p interface{}) ([]byte, error) {
	if ps, err := json.Marshal(p); err != nil {
		return nil, errors.New(fmt.Sprintf("unable to marshal poliy %v to a string, error %v", p, err))
	} else {
		hash := sha3.Sum256([]byte(ps))
		return hash[:], nil
	}
}

// Add a service policy to a BusinessPolicyEntry
func (p *BusinessPolicyEntry) AddServicePolicy(svcPolicy *externalpolicy.ExternalPolicy, svcId string) error {
	if svcPolicy == nil || svcId == "" {
		return nil
	}

	pSE, err := NewServicePolicyEntry(svcPolicy, svcId)
	if err != nil {
		return err
	}
	p.ServicePolicies[svcId] = pSE
	return nil
}

func (pe *BusinessPolicyEntry) DeleteAllServicePolicies(org string) {
	pe.ServicePolicies = make(map[string]*ServicePolicyEntry, 0)
}

func (p *BusinessPolicyEntry) UpdateEntry(pol *businesspolicy.BusinessPolicy, polId string, newHash []byte) (*policy.Policy, error) {
	p.Hash = newHash
	p.Updated = uint64(time.Now().Unix())
	p.ServicePolicies = make(map[string]*ServicePolicyEntry, 0)

	// validate and convert the exchange business policy to internal policy format
	if err := pol.Validate(); err != nil {
		return nil, fmt.Errorf("Failed to validate the business policy %v. %v", *pol, err)
	} else if pPolicy, err := pol.GenPolicyFromBusinessPolicy(polId); err != nil {
		return nil, fmt.Errorf("Failed to convert the business policy to internal policy format: %v. %v", *pol, err)
	} else {
		p.Policy = pPolicy
		return pPolicy, nil
	}
}

type PolicyManager struct {
	spMapLock      sync.Mutex                                 // The lock that protects the map of ServedPolicies because it is referenced from another thread.
	polMapLock     sync.Mutex                                 // The lock that protects the map of BusinessPolicyEntry because it is referenced from another thread.
	eventChannel   chan events.Message                        // for sending policy change messages
	ServedPolicies map[string]exchange.ServedBusinessPolicy   // served node org, business policy org and business policy triplets. The key is the triplet exchange id.
	OrgPolicies    map[string]map[string]*BusinessPolicyEntry // all served policies by this agbot. The first key is org, the second key is business policy exchange id without org.
}

func (pm *PolicyManager) String() string {
	pm.polMapLock.Lock()
	defer pm.polMapLock.Unlock()

	res := "Policy Manager: "
	for org, orgMap := range pm.OrgPolicies {
		res += fmt.Sprintf("Org: %v ", org)
		for pat, pe := range orgMap {
			res += fmt.Sprintf("Business policy: %v %v ", pat, pe)
		}
	}

	pm.spMapLock.Lock()
	defer pm.spMapLock.Unlock()

	for _, served := range pm.ServedPolicies {
		res += fmt.Sprintf(" Serve: %v ", served)
	}
	return res
}

func (pm *PolicyManager) ShortString() string {
	pm.polMapLock.Lock()
	defer pm.polMapLock.Unlock()

	res := "Policy Manager: "
	for org, orgMap := range pm.OrgPolicies {
		res += fmt.Sprintf("Org: %v ", org)
		for pat, pe := range orgMap {
			s := ""
			if pe != nil {
				s = pe.ShortString()
			}
			res += fmt.Sprintf("Business policy: %v %v ", pat, s)
		}
	}
	return res
}

func NewPolicyManager(eventChannel chan events.Message) *PolicyManager {
	pm := &PolicyManager{
		OrgPolicies:  make(map[string]map[string]*BusinessPolicyEntry),
		eventChannel: eventChannel,
	}
	return pm
}

func (pm *PolicyManager) hasOrg(org string) bool {
	if _, ok := pm.OrgPolicies[org]; ok {
		return true
	}
	return false
}

func (pm *PolicyManager) hasBusinessPolicy(org string, polName string) bool {
	if pm.hasOrg(org) {
		if _, ok := pm.OrgPolicies[org][polName]; ok {
			return true
		}
	}
	return false
}

func (pm *PolicyManager) GetAllBusinessPolicyEntriesForOrg(org string) map[string]*BusinessPolicyEntry {
	pm.polMapLock.Lock()
	defer pm.polMapLock.Unlock()

	if pm.hasOrg(org) {
		return pm.OrgPolicies[org]
	}
	return nil
}

func (pm *PolicyManager) GetAllPolicyOrgs() []string {
	pm.polMapLock.Lock()
	defer pm.polMapLock.Unlock()

	orgs := make([]string, 0)
	for org, _ := range pm.OrgPolicies {
		orgs = append(orgs, org)
	}
	return orgs
}

// copy the given map of served business policies
func (pm *PolicyManager) setServedBusinessPolicies(servedPols map[string]exchange.ServedBusinessPolicy) {
	pm.spMapLock.Lock()
	defer pm.spMapLock.Unlock()

	// copy the input map
	pm.ServedPolicies = servedPols
}

// chek if the agbot serves the given business policy or not.
func (pm *PolicyManager) serveBusinessPolicy(polOrg string, polName string) bool {
	pm.spMapLock.Lock()
	defer pm.spMapLock.Unlock()

	for _, sp := range pm.ServedPolicies {
		if sp.BusinessPolOrg == polOrg && (sp.BusinessPol == polName || sp.BusinessPol == "*") {
			return true
		}
	}
	return false
}

// check if the agbot service the given org or not.
func (pm *PolicyManager) serveOrg(polOrg string) bool {
	pm.spMapLock.Lock()
	defer pm.spMapLock.Unlock()

	for _, sp := range pm.ServedPolicies {
		if sp.BusinessPolOrg == polOrg {
			return true
		}
	}
	return false
}

// return an array of node orgs for the given served policy org and policy.
// this function is called from a different thread.
func (pm *PolicyManager) GetServedNodeOrgs(polOrg string, polName string) []string {
	pm.spMapLock.Lock()
	defer pm.spMapLock.Unlock()

	node_orgs := []string{}
	for _, sp := range pm.ServedPolicies {
		if sp.BusinessPolOrg == polOrg && (sp.BusinessPol == polName || sp.BusinessPol == "*") {
			node_org := sp.NodeOrg
			// the default node org is the policy org
			if node_org == "" {
				node_org = sp.BusinessPolOrg
			}
			node_orgs = append(node_orgs, node_org)
		}
	}
	return node_orgs
}

// Given a list of policy_org/policy/node_org triplets that this agbot is supposed to serve, save that list and
// convert it to map of maps (keyed by org and policy name) to hold all the policy meta data. This
// will allow the PolicyManager to know when the policy metadata changes.
func (pm *PolicyManager) SetCurrentBusinessPolicies(servedPols map[string]exchange.ServedBusinessPolicy) error {
	pm.polMapLock.Lock()
	defer pm.polMapLock.Unlock()

	// Exit early if nothing to do
	if len(pm.ServedPolicies) == 0 && len(servedPols) == 0 {
		return nil
	}

	// save the served business policies in the pm
	pm.setServedBusinessPolicies(servedPols)

	// Create a new map of maps
	if len(pm.OrgPolicies) == 0 {
		pm.OrgPolicies = make(map[string]map[string]*BusinessPolicyEntry)
	}

	// For each org that this agbot is supposed to be serving, check if it is already in the pm.
	// If not add to it. The policies will be added later in the UpdatePolicies function.
	for _, served := range servedPols {
		// If we have encountered a new org in the served policy list, create a map of policies for it.
		if !pm.hasOrg(served.BusinessPolOrg) {
			pm.OrgPolicies[served.BusinessPolOrg] = make(map[string]*BusinessPolicyEntry)
		}
	}

	// For each org in the existing PolicyManager, check to see if its in the new map. If not, then
	// this agbot is no longer serving any business polices in that org, we can get rid of everything in that org.
	for org, _ := range pm.OrgPolicies {
		if !pm.serveOrg(org) {
			// delete org and all policy files in it.
			glog.V(5).Infof("Deletinging the org %v from the policy manager because it is no longer hosted by the agbot.", org)
			if err := pm.deleteOrg(org); err != nil {
				return err
			}
		}
	}

	return nil
}

// For each org that the agbot is supporting, take the set of business policies defined within the org and save them into
// the PolicyManager. When new or updated policies are discovered, clear ServicePolicies for that BusinessPolicyEntry so that
// new businees polices can be filled later.
func (pm *PolicyManager) UpdatePolicies(org string, definedPolicies map[string]exchange.ExchangeBusinessPolicy) error {
	pm.polMapLock.Lock()
	defer pm.polMapLock.Unlock()

	// Exit early on error
	if !pm.hasOrg(org) {
		return errors.New(fmt.Sprintf("org %v not found in policy manager", org))
	}

	// If there is no business policy in the org, delete the org from the pm and all of the policy files in the org.
	// This is the case where business policy or the org has been deleted but the agbot still hosts the policy on the exchange.
	if definedPolicies == nil || len(definedPolicies) == 0 {
		// delete org and all policy files in it.
		glog.V(5).Infof("Deletinging the org %v from the policy manager because it does not contain a business policy.", org)
		return pm.deleteOrg(org)
	}

	// Delete the business policy from the pm if the policy does not exist on the exchange or the agbot
	// does not serve it any more.
	for polName, _ := range pm.OrgPolicies[org] {
		need_delete := true
		if pm.serveBusinessPolicy(org, polName) {
			for polId, _ := range definedPolicies {
				if exchange.GetId(polId) == polName {
					need_delete = false
					break
				}
			}
		}

		if need_delete {
			glog.V(5).Infof("Deletinging business policy %v from the org %v from the policy manager because the policy no longer exists.", polName, org)
			if err := pm.deleteBusinessPolicy(org, polName); err != nil {
				return err
			}
		}
	}

	// Now we just need to handle adding new business policies or update existing business policies
	for polId, exPol := range definedPolicies {
		pol := exPol.GetBusinessPolicy()
		if !pm.serveBusinessPolicy(org, exchange.GetId(polId)) {
			continue
		}

		need_new_entry := true
		if pm.hasBusinessPolicy(org, exchange.GetId(polId)) {
			if pe := pm.OrgPolicies[org][exchange.GetId(polId)]; pe != nil {
				need_new_entry = false

				// The PolicyEntry is already there, so check if the policy definition has changed.
				// If the policy has changed, Send a PolicyChangedMessage message. Otherwise the policy
				// definition we have is current.
				newHash, err := hashPolicy(&pol)
				if err != nil {
					return errors.New(fmt.Sprintf("unable to hash the business policy %v for %v, error %v", pol, org, err))
				}
				if !bytes.Equal(pe.Hash, newHash) {
					// update the cache
					glog.V(5).Infof("Updating policy entry for %v of org %v because it is changed. ", polId, org)
					newPol, err := pe.UpdateEntry(&pol, polId, newHash)
					if err != nil {
						return errors.New(fmt.Sprintf("error updating business policy entry for %v of org %v: %v", polId, org, err))
					}

					// send a message so that other process can handle it by re-negotiating agreements
					glog.V(3).Infof(fmt.Sprintf("Policy manager detected changed business policy %v", polId))
					if policyString, err := policy.MarshalPolicy(newPol); err != nil {
						glog.Errorf(fmt.Sprintf("Error trying to marshal policy %v error: %v", newPol, err))
					} else {
						pm.eventChannel <- events.NewPolicyChangedMessage(events.CHANGED_POLICY, "", newPol.Header.Name, org, policyString)
					}
				}
			}
		}

		//If there's no BusinessPolicyEntry yet, create one
		if need_new_entry {
			if newPE, err := NewBusinessPolicyEntry(&pol, polId); err != nil {
				return errors.New(fmt.Sprintf("unable to create business policy entry for %v, error %v", pol, err))
			} else {
				pm.OrgPolicies[org][exchange.GetId(polId)] = newPE
			}
		}
	}

	return nil
}

// When an org is removed from the list of supported orgs and business policies, remove the org
// from the PolicyManager.
func (pm *PolicyManager) deleteOrg(org_in string) error {
	// send PolicyDeletedMessage message for each business polices in the org
	for org, orgMap := range pm.OrgPolicies {
		if org == org_in {
			for polName, pe := range orgMap {
				if pe != nil {
					glog.V(3).Infof(fmt.Sprintf("Policy manager detected deleted policy %v", polName))
					if policyString, err := policy.MarshalPolicy(pe.Policy); err != nil {
						glog.Errorf(fmt.Sprintf("Policy manager error trying to marshal policy %v error: %v", polName, err))
					} else {
						pm.eventChannel <- events.NewPolicyDeletedMessage(events.DELETED_POLICY, "", pe.Policy.Header.Name, org, policyString)
					}
				}
			}
			break
		}
	}

	// Get rid of the org map
	if pm.hasOrg(org_in) {
		delete(pm.OrgPolicies, org_in)
	}
	return nil
}

// When a business policy is removed from the exchange, remove it from the PolicyManager and send a PolicyDeletedMessage.
func (pm *PolicyManager) deleteBusinessPolicy(org string, polName string) error {
	// Get rid of the business policy from the pm
	if pm.hasOrg(org) {
		if pe, ok := pm.OrgPolicies[org][polName]; ok {
			if pe != nil {
				glog.V(3).Infof(fmt.Sprintf("Policy manager detected deleted policy %v", polName))
				if policyString, err := policy.MarshalPolicy(pe.Policy); err != nil {
					glog.Errorf(fmt.Sprintf("Policy manager error trying to marshal policy %v error: %v", polName, err))
				} else {
					pm.eventChannel <- events.NewPolicyDeletedMessage(events.DELETED_POLICY, "", pe.Policy.Header.Name, org, policyString)
				}
			}

			delete(pm.OrgPolicies[org], polName)
		}
	}

	return nil
}
