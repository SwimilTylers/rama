package remotecluster

import (
	"context"
	"errors"
	"fmt"
	"github.com/oecp/rama/pkg/feature"
	"runtime"
	"testing"
	"time"

	"github.com/oecp/rama/pkg/client/clientset/versioned"
	restclient "k8s.io/client-go/rest"

	runtimeutil "k8s.io/apimachinery/pkg/util/runtime"

	jsoniter "github.com/json-iterator/go"
	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

func TestWatchRemoteCluster(t *testing.T) {
	config, err := clientconfig.GetConfig()
	assert.Nil(t, err)
	kubeClient := kubernetes.NewForConfigOrDie(config)

	informerFactory := informers.NewSharedInformerFactory(kubeClient, 0)
	secretInformer := informerFactory.Core().V1().Secrets()
	secretLister := informerFactory.Core().V1().Secrets().Lister()
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "secret")

	addFunc := func(obj interface{}) {
		t.Log("add func")
		secret, ok := obj.(*apiv1.Secret)
		if !ok {
			t.Fatal("Convert not ok")
		}
		s, err := jsoniter.MarshalToString(secret)
		t.Log(s, err)
		queue.Add(secret.Name)
	}
	updateFunc := func(oldObj, newObj interface{}) {
		t.Log("update func")
		secret, ok := newObj.(*apiv1.Secret)
		if !ok {
			t.Fatal("Convert not ok")
		}
		s, err := jsoniter.MarshalToString(secret)
		t.Log(s, err)
		queue.Add(secret.Name)
	}

	process := func() bool {
		t.Log("before queue getting")
		obj, shutdown := queue.Get()
		if shutdown {
			return false
		}
		s, _ := obj.(string)
		secret, err := secretLister.Secrets("default").Get(s)
		t.Log(secret)
		t.Log(err)
		return true
	}

	run := func() {
		t.Log("running")
		for process() {
			t.Log("process once")
		}
	}
	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    addFunc,
		UpdateFunc: updateFunc,
		DeleteFunc: addFunc,
	})
	ch := make(chan struct{})
	now := time.Now()
	t.Log("start sync")
	t.Log(secretInformer.Informer().HasSynced())
	informerFactory.Start(ch)
	if ok := cache.WaitForCacheSync(ch, secretInformer.Informer().HasSynced); !ok {
		t.Error("failed to wait for caches to sync")
	}
	t.Logf("sync success. spent: %v", time.Since(now))

	go wait.Until(run, 1*time.Second, ch)
	<-ch
}

func TestNilSecret(t *testing.T) {
	config, err := clientconfig.GetConfig()
	assert.Nil(t, err)
	kubeClient := kubernetes.NewForConfigOrDie(config)
	infFac := informers.NewSharedInformerFactory(kubeClient, 0)
	lister := infFac.Core().V1().Secrets().Lister().Secrets("default")
	secret, err := lister.Get("notexist")
	t.Log(err)
	t.Log(secret)
}

func TestNilList(t *testing.T) {
	config, err := clientconfig.GetConfig()
	assert.Nil(t, err)
	kubeClient := kubernetes.NewForConfigOrDie(config)
	infFac := informers.NewSharedInformerFactory(kubeClient, 0)
	secretInformer := infFac.Core().V1().Secrets().Informer()
	ch := make(chan struct{})
	go func() {
		//if ok := cache.WaitForCacheSync(ch, infFac.Core().V1().Secrets().Informer().HasSynced); !ok {
		if ok := cache.WaitForCacheSync(ch, secretInformer.HasSynced); !ok {
			t.Errorf("failed to wait for caches to sync")
		} else {
			t.Log("wait finish")
			lister := infFac.Core().V1().Secrets().Lister().Secrets("default")
			secret, err := lister.List(labels.SelectorFromSet(labels.Set{}))
			t.Log(err, secret)
			s, err := lister.Get("default-token-8njl6")
			t.Log(err, s)
		}
	}()
	go infFac.Start(ch)

	<-ch
}

func TestWaitUntil(t *testing.T) {
	ch := make(chan struct{})
	wait.Until(printHelloWorld, 1*time.Second, ch)
}

func printHelloWorld() {
	time.Sleep(2 * time.Second)
	fmt.Println("hello world", time.Now())
}

func TestWaitUtil2(t *testing.T) {
	ch := make(chan struct{})
	go wait.Until(printLong, 1*time.Second, ch)
	go wait.Until(goroutine, 1*time.Second, ch)
	go wait.Until(forloop, 1*time.Second, ch)
	<-ch
}

func printLong() {
	time.Sleep(100 * time.Second)
	fmt.Println("hello world", time.Now())
}

func goroutine() {
	fmt.Println("goroutine num:", runtime.NumGoroutine())
}

func forloop() {
	for {
		fmt.Println("forloop")
	}
}

func TestWaitUtil3(t *testing.T) {
	ch := make(chan struct{})
	go wait.Until(father, 1*time.Second, ch)
	time.Sleep(2 * time.Second)
	close(ch)
	fmt.Println("father over")
	time.Sleep(10 * time.Second)
}

func father() {
	i := 0
	for ; i < 10; i++ {
		i := i
		go func() {
			for {
				time.Sleep(1 * time.Second)
				fmt.Println("hello from ", i)
			}
		}()
	}
}

func TestErrKubeI(t *testing.T) {
	config, err := clientconfig.GetConfig()
	t.Log(err)
	kubeClient := kubernetes.NewForConfigOrDie(config)
	body, err := kubeClient.DiscoveryClient.RESTClient().Get().AbsPath("/healthz").Do(context.TODO()).Raw()
	t.Log(err)
	t.Log(string(body))
}

func TestLabelSelector(t *testing.T) {
	config, err := clientconfig.GetConfig()
	kubeClient := kubernetes.NewForConfigOrDie(config)

	informerFactory := informers.NewSharedInformerFactory(kubeClient, 0)
	//secretInformer := informerFactory.Core().V1().Secrets()
	secretLister := informerFactory.Core().V1().Secrets().Lister()
	//queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "secret")
	secret, err := secretLister.Secrets("default").Get("hello-world")
	if k8serror.IsNotFound(err) {
		t.Log("hello")
	}
	t.Log(secret, err)
}

func TestConnection(t *testing.T) {
	const (
		ByDefaultNS = "defaultnamespace"
	)
	config, err := clientconfig.GetConfig()
	assert.Nil(t, err)
	kubeClient := kubernetes.NewForConfigOrDie(config)

	informerFactory := informers.NewSharedInformerFactory(kubeClient, 0)
	secretInformer := informerFactory.Core().V1().Secrets()
	secretLister := informerFactory.Core().V1().Secrets().Lister()
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "secret")

	addFunc := func(obj interface{}) {
		t.Log("add func", secretInformer.Informer().HasSynced())
		secret, ok := obj.(*apiv1.Secret)
		if !ok {
			t.Fatal("Convert not ok")
		}
		s, err := jsoniter.MarshalToString(secret)
		t.Log(s, err)
		queue.Add(secret.Name)
	}
	updateFunc := func(oldObj, newObj interface{}) {
		t.Log("update func")
		secret, ok := newObj.(*apiv1.Secret)
		if !ok {
			t.Fatal("Convert not ok")
		}
		s, err := jsoniter.MarshalToString(secret)
		t.Log(s, err)
		queue.Add(secret.Name)
	}

	process := func() bool {
		t.Log("before queue getting")
		obj, shutdown := queue.Get()
		if shutdown {
			return false
		}
		s, _ := obj.(string)
		secret, err := secretLister.Secrets("default").Get(s)
		t.Log(secret)
		t.Log(err)
		return true
	}

	run := func() {
		for process() {
		}
	}
	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    addFunc,
		UpdateFunc: updateFunc,
		DeleteFunc: addFunc,
	})
	if err = secretInformer.Informer().GetIndexer().AddIndexers(cache.Indexers{
		ByDefaultNS: func(obj interface{}) ([]string, error) {
			return []string{"louhwz"}, nil
		},
	}); err != nil {
		panic(err)
	}

	ch := make(chan struct{})
	now := time.Now()
	t.Log("start sync")
	t.Log(secretInformer.Informer().HasSynced())

	go func() {
		if ok := cache.WaitForCacheSync(ch, secretInformer.Informer().HasSynced); !ok {
			t.Error("failed to wait for caches to sync")
		}
	}()
	go informerFactory.Start(ch)

	t.Logf("sync success. spent: %v", time.Since(now))
	go func() {
		for {
			if secretInformer.Informer().HasSynced() {
				i, e := secretInformer.Informer().GetIndexer().ByIndex(ByDefaultNS, "louhwz")
				//assert.Nil(t, e)
				t.Log(e)
				//_, _ := jsoniter.MarshalToString(i)
				t.Log("len=", len(i))

				i, e = secretInformer.Informer().GetIndexer().ByIndex(ByDefaultNS, "louhwz11")
				//assert.Nil(t, e)
				t.Log(e)
				//_, _ := jsoniter.MarshalToString(i)
				t.Log("len=", len(i), ", second")
				time.Sleep(1 * time.Second)
			}
		}
	}()
	go wait.Until(run, 1*time.Second, ch)
	<-ch
}

func TestDeleteExistResource(t *testing.T) {
	config, err := clientconfig.GetConfig()
	assert.Nil(t, err)
	kubeClient := kubernetes.NewForConfigOrDie(config)
	err = kubeClient.CoreV1().Pods("default").Delete(context.TODO(), "louhwz", metav1.DeleteOptions{})
	if k8serror.IsNotFound(err) {
		t.Log("not found")
	}
}

func TestNewSelector(t *testing.T) {
	config, err := clientconfig.GetConfig()
	assert.Nil(t, err)
	kubeClient := kubernetes.NewForConfigOrDie(config)
	infFac := informers.NewSharedInformerFactory(kubeClient, 0)
	_ = infFac.Core().V1().Secrets().Informer()
	ch := make(chan struct{})
	go func() {
		//if ok := cache.WaitForCacheSync(ch, infFac.Core().V1().Secrets().Informer().HasSynced); !ok {
		if ok := cache.WaitForCacheSync(ch, infFac.Core().V1().Secrets().Informer().HasSynced); !ok {
			t.Errorf("failed to wait for caches to sync")
		} else {
			t.Log("wait finish")
			lister := infFac.Core().V1().Secrets().Lister().Secrets("default")
			secret, err := lister.List(labels.NewSelector())
			t.Log(err, secret)
			s, err := lister.Get("default-token-8njl6")
			t.Log(err, s)
		}
	}()
	go infFac.Start(ch)

	<-ch
}

func TestRunTimeHandleCrash(t *testing.T) {
	c()
	t.Log("yqz")
}

func c() {
	defer runtimeutil.HandleCrash()

	panic("hello")

}

func TestRecover(t *testing.T) {
	err := c2()
	t.Log("yqz")
	t.Log(err)
}

func c2() error {
	defer func() {
		if err := recover(); err != nil {

		}
	}()
	panic("hello")
}

//
//func TestNilIndexer(t *testing.T) {
//	const IndexName = "index"
//	config, _ := clientconfig.GetConfig()
//	kubeClient := kubernetes.NewForConfigOrDie(config)
//	infFac := informers.NewSharedInformerFactory(kubeClient, 0)
//	secretInformer := infFac.Core().V1().Secrets().Informer()
//	secretInformer.GetIndexer().AddIndexers(cache.Indexers{
//		IndexName:
//	})
//	ch := make(chan struct{})
//	go func() {
//		//if ok := cache.WaitForCacheSync(ch, infFac.Core().V1().Secrets().Informer().HasSynced); !ok {
//		if ok := cache.WaitForCacheSync(ch, infFac.Core().V1().Secrets().Informer().HasSynced); !ok {
//			t.Errorf("failed to wait for caches to sync")
//		} else {
//			t.Log("wait finish")
//			lister := infFac.Core().V1().Secrets().Lister().Secrets("default")
//			secret, err := lister.List(labels.NewSelector())
//			t.Log(err, secret)
//			s, err := lister.Get("default-token-8njl6")
//			t.Log(err, s)
//		}
//	}()
//	go infFac.Start(ch)
//
//	<-ch
//}

func TestNilClientInterface(t *testing.T) {
	config, err := clientconfig.GetConfig()
	assert.Nil(t, err)
	//kubeClient := kubernetes.NewForConfigOrDie(config)
	ramaClient := versioned.NewForConfigOrDie(restclient.AddUserAgent(config, "Rama"))

	_, err = ramaClient.NetworkingV1().RemoteSubnets().Get(context.TODO(), "subnet", metav1.GetOptions{})
	if k8serror.IsNotFound(err) {
		t.Log("hello world")
	}
	assert.Nil(t, err)
	//t.Log(string(body))
}

func TestHandleError(t *testing.T) {
	err := errors.New("err happened")
	runtimeutil.HandleError(err)
}

func MarshalToString(i interface{}) string {
	s, _ := jsoniter.MarshalToString(i)
	return s
}

func TestUUID(t *testing.T) {
	config, err := clientconfig.GetConfig()
	assert.Nil(t, err)
	//kubeClient := kubernetes.NewForConfigOrDie(config)
	ramaClient := versioned.NewForConfigOrDie(restclient.AddUserAgent(config, "Rama"))

	rv, err := ramaClient.NetworkingV1().RemoteVteps().List(context.TODO(), metav1.ListOptions{})
	t.Log(err)
	t.Log(MarshalToString(rv))

	vtep, err := ramaClient.NetworkingV1().RemoteVteps().Get(context.TODO(), "remotecluster-2.kube-salve1", metav1.GetOptions{})
	t.Log(err)
	t.Log(MarshalToString(vtep))

}

func TestEnabled(t *testing.T) {
	t.Log(feature.MultiClusterEnabled())
}
