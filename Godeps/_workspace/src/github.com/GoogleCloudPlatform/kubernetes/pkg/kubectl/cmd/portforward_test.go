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

package cmd

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
)

type fakePortForwarder struct {
	req   *client.Request
	pfErr error
}

func (f *fakePortForwarder) ForwardPorts(req *client.Request, config *client.Config, ports []string, stopChan <-chan struct{}) error {
	f.req = req
	return f.pfErr
}

func TestPortForward(t *testing.T) {

	tests := []struct {
		name, version, podPath, pfPath, container string
		nsInQuery                                 bool
		pod                                       *api.Pod
		pfErr                                     bool
	}{
		{
			name:      "v1beta1 - pod portforward",
			version:   "v1beta1",
			podPath:   "/api/v1beta1/pods/foo",
			pfPath:    "/api/v1beta1/pods/foo/portforward",
			nsInQuery: true,
			pod:       execPod(),
		},
		{
			name:      "v1beta3 - pod portforward",
			version:   "v1beta3",
			podPath:   "/api/v1beta3/namespaces/test/pods/foo",
			pfPath:    "/api/v1beta3/namespaces/test/pods/foo/portforward",
			nsInQuery: false,
			pod:       execPod(),
		},
		{
			name:      "v1beta3 - pod portforward error",
			version:   "v1beta3",
			podPath:   "/api/v1beta3/namespaces/test/pods/foo",
			pfPath:    "/api/v1beta3/namespaces/test/pods/foo/portforward",
			nsInQuery: false,
			pod:       execPod(),
			pfErr:     true,
		},
	}
	for _, test := range tests {
		f, tf, codec := NewAPIFactory()
		tf.Client = &client.FakeRESTClient{
			Codec: codec,
			Client: client.HTTPClientFunc(func(req *http.Request) (*http.Response, error) {
				switch p, m := req.URL.Path, req.Method; {
				case p == test.podPath && m == "GET":
					if test.nsInQuery {
						if ns := req.URL.Query().Get("namespace"); ns != "test" {
							t.Errorf("%s: did not get expected namespace: %s\n", test.name, ns)
						}
					}
					body := objBody(codec, test.pod)
					return &http.Response{StatusCode: 200, Body: body}, nil
				default:
					// Ensures no GET is performed when deleting by name
					t.Errorf("%s: unexpected request: %#v\n%#v", test.name, req.URL, req)
					return nil, nil
				}
			}),
		}
		tf.Namespace = "test"
		tf.ClientConfig = &client.Config{Version: test.version}
		ff := &fakePortForwarder{}
		if test.pfErr {
			ff.pfErr = fmt.Errorf("pf error")
		}
		cmd := &cobra.Command{}
		podPtr := cmd.Flags().StringP("pod", "p", "", "Pod name")
		*podPtr = "foo"
		err := RunPortForward(f, cmd, []string{":5000", ":1000"}, ff)
		if test.pfErr && err != ff.pfErr {
			t.Errorf("%s: Unexpected exec error: %v", test.name, err)
		}
		if !test.pfErr && ff.req.URL().Path != test.pfPath {
			t.Errorf("%s: Did not get expected path for portforward request", test.name)
		}
		if !test.pfErr && err != nil {
			t.Errorf("%s: Unexpected error: %v", test.name, err)
		}
	}
}
