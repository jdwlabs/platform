package k8s

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// NewFake returns a fake.Clientset seeded with the supplied objects. Used by
// every unit test in this package and downstream packages.
func NewFake(objs ...runtime.Object) kubernetes.Interface {
	return fake.NewSimpleClientset(objs...)
}
