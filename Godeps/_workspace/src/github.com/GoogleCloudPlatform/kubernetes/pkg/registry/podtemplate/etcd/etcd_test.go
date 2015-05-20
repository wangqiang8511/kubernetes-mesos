/*
Copyright 2014 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package etcd

import (
	"testing"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/rest/resttest"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/v1beta3"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/tools"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/tools/etcdtest"
)

func newHelper(t *testing.T) (*tools.FakeEtcdClient, tools.EtcdHelper) {
	fakeEtcdClient := tools.NewFakeEtcdClient(t)
	fakeEtcdClient.TestIndex = true
	helper := tools.NewEtcdHelper(fakeEtcdClient, v1beta3.Codec, etcdtest.PathPrefix())
	return fakeEtcdClient, helper
}

func validNewPodTemplate(name string) *api.PodTemplate {
	return &api.PodTemplate{
		ObjectMeta: api.ObjectMeta{
			Name:      name,
			Namespace: api.NamespaceDefault,
		},
		Template: api.PodTemplateSpec{
			ObjectMeta: api.ObjectMeta{
				Labels: map[string]string{"test": "foo"},
			},
			Spec: api.PodSpec{
				RestartPolicy: api.RestartPolicyAlways,
				DNSPolicy:     api.DNSClusterFirst,
				Containers: []api.Container{
					{
						Name:            "foo",
						Image:           "test",
						ImagePullPolicy: api.PullAlways,

						TerminationMessagePath: api.TerminationMessagePathDefault,
					},
				},
			},
		},
	}
}

func TestCreate(t *testing.T) {
	fakeEtcdClient, helper := newHelper(t)
	storage := NewREST(helper)
	test := resttest.New(t, storage, fakeEtcdClient.SetError)
	pod := validNewPodTemplate("foo")
	pod.ObjectMeta = api.ObjectMeta{}
	test.TestCreate(
		// valid
		pod,
		// invalid
		&api.PodTemplate{
			Template: api.PodTemplateSpec{},
		},
	)
}

func TestUpdate(t *testing.T) {
	fakeEtcdClient, helper := newHelper(t)
	storage := NewREST(helper)
	test := resttest.New(t, storage, fakeEtcdClient.SetError)
	key := etcdtest.AddPrefix("podtemplates/default/foo")

	fakeEtcdClient.ExpectNotFoundGet(key)
	fakeEtcdClient.ChangeIndex = 2
	pod := validNewPodTemplate("foo")
	existing := validNewPodTemplate("exists")
	obj, err := storage.Create(api.NewDefaultContext(), existing)
	if err != nil {
		t.Fatalf("unable to create object: %v", err)
	}
	older := obj.(*api.PodTemplate)
	older.ResourceVersion = "1"

	test.TestUpdate(
		pod,
		existing,
		older,
	)
}
