package providers

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lawrencegripper/ion/dispatcher/messaging"
	"github.com/lawrencegripper/ion/dispatcher/types"
	log "github.com/sirupsen/logrus"

	batchv1 "k8s.io/api/batch/v1"
)

const (
	mockDispatcherName = "mockdispatchername"
	mockMessageID      = "examplemessageID"
)

func NewMockKubernetesProvider(create func(b *batchv1.Job) (*batchv1.Job, error), list func() (*batchv1.JobList, error)) (*Kubernetes, error) {
	k := Kubernetes{}

	k.Namespace = "module-ns"
	k.jobConfig = &types.JobConfig{
		SidecarImage: "sidecar-image",
		WorkerImage:  "worker-image",
	}
	k.dispatcherName = mockDispatcherName

	k.inflightJobStore = map[string]messaging.Message{}
	k.createJob = create
	k.listAllJobs = list
	return &k, nil
}

func TestGetJobName(t *testing.T) {
	messageToSend := MockMessage{
		MessageID:          mockMessageID,
		DeliveryCountValue: 15,
	}
	jobName := getJobName(messageToSend)

	if !strings.Contains(jobName, "15") {
		t.Error("Should contain the attempt count")
	}

	if !strings.Contains(jobName, strings.ToLower(messageToSend.MessageID)) {
		t.Error("Should contain messageID")
	}
	if strings.ToLower(jobName) != jobName {
		t.Logf("contained upper case")
		t.FailNow()
	}
}

func TestDispatchAddsJob(t *testing.T) {
	inMemMockJobStore := []batchv1.Job{}

	create := func(b *batchv1.Job) (*batchv1.Job, error) {
		inMemMockJobStore = append(inMemMockJobStore, *b)
		return b, nil
	}

	list := func() (*batchv1.JobList, error) {
		return &batchv1.JobList{
			Items: inMemMockJobStore,
		}, nil
	}

	k, _ := NewMockKubernetesProvider(create, list)

	messageToSend := MockMessage{
		MessageID: mockMessageID,
	}

	err := k.Dispatch(messageToSend)

	if err != nil {
		t.Error(err)
	}

	jobsLen := len(inMemMockJobStore)
	if jobsLen != 1 {
		t.Errorf("Job count incorrected Expected: 1 Got: %v", jobsLen)
	}
}

func TestFailedDispatchRejectsMessage(t *testing.T) {
	inMemMockJobStore := []batchv1.Job{}

	create := func(b *batchv1.Job) (*batchv1.Job, error) {
		return nil, fmt.Errorf("Failed to send: %v", b)
	}

	list := func() (*batchv1.JobList, error) {
		return &batchv1.JobList{
			Items: inMemMockJobStore,
		}, nil
	}

	k, _ := NewMockKubernetesProvider(create, list)

	wasRejected := false
	messageToSend := MockMessage{
		MessageID: mockMessageID,
	}
	messageToSend.Rejected = func() {
		wasRejected = true
	}

	err := k.Dispatch(messageToSend)

	if err == nil {
		t.Error("Expected error ... didn't see one!")
	}

	if !wasRejected {
		t.Error("Expected to be rejected... wasn't")
	}

	if len(inMemMockJobStore) > 0 {
		t.Error("Expected job to not be stored")
	}
}

func TestDispatchedJobConfiguration(t *testing.T) {
	inMemMockJobStore := []batchv1.Job{}

	create := func(b *batchv1.Job) (*batchv1.Job, error) {
		inMemMockJobStore = append(inMemMockJobStore, *b)
		return b, nil
	}

	list := func() (*batchv1.JobList, error) {
		return &batchv1.JobList{
			Items: inMemMockJobStore,
		}, nil
	}

	k, _ := NewMockKubernetesProvider(create, list)

	messageToSend := MockMessage{
		MessageID: mockMessageID,
	}

	err := k.Dispatch(messageToSend)

	if err != nil {
		t.Error(err)
	}

	job := inMemMockJobStore[0]

	CheckLabelsAssignedCorrectly(t, job, messageToSend.MessageID)
	CheckPodSetup(t, job, k.jobConfig.SidecarImage, k.jobConfig.WorkerImage)
}

func CheckLabelsAssignedCorrectly(t *testing.T, job batchv1.Job, expectedMessageID string) {
	testCases := []struct {
		labelName     string
		expectedValue string
	}{
		{
			labelName:     dispatcherNameLabel,
			expectedValue: mockDispatcherName,
		},
		{
			labelName:     messageIDLabel,
			expectedValue: expectedMessageID,
		},
		{
			labelName:     deliverycountlabel,
			expectedValue: "0",
		},
	}

	for _, test := range testCases {
		test := test
		t.Run("Set:"+test.labelName, func(t *testing.T) {
			t.Parallel()

			value, ok := job.Labels[test.labelName]
			if !ok {
				t.Error("missing dispatchername label")
			}
			if value != test.expectedValue {
				t.Errorf("wrong dispatchername label Expected: %s Got: %s", test.expectedValue, value)
			}
		})
	}
}

func CheckPodSetup(t *testing.T, job batchv1.Job, expectedSidecarImage, expectedWorkerImage string) {
	sidecar := job.Spec.Template.Spec.Containers[0]
	if sidecar.Image != expectedSidecarImage {
		t.Errorf("sidecar image wrong Got: %s Expected: %s", sidecar.Image, expectedSidecarImage)
	}
	worker := job.Spec.Template.Spec.Containers[1]
	if worker.Image != expectedWorkerImage {
		t.Errorf("worker image wrong Got: %s Expected: %s", worker.Image, expectedWorkerImage)
	}
}

func TestReconcileJobCompleted(t *testing.T) {
	//Setup... it's a long one. We need to schedule a job first
	inMemMockJobStore := []batchv1.Job{}

	create := func(b *batchv1.Job) (*batchv1.Job, error) {
		inMemMockJobStore = append(inMemMockJobStore, *b)
		return b, nil
	}

	list := func() (*batchv1.JobList, error) {
		return &batchv1.JobList{
			Items: inMemMockJobStore,
		}, nil
	}

	k, _ := NewMockKubernetesProvider(create, list)

	var acceptedMessage bool

	messageToSend := MockMessage{
		MessageID: mockMessageID,
		Accepted: func() {
			acceptedMessage = true
		},
	}

	err := k.Dispatch(messageToSend)
	if err != nil {
		t.Error(err)
	}

	job := &inMemMockJobStore[0]
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type: batchv1.JobComplete,
	})
	//Lets test things...
	err = k.Reconcile()
	if err != nil {
		t.Error(err)
	}

	if !acceptedMessage {
		t.Error("Failed to accept message during reconcilation. Expected message to be marked as accepted as job is complete")
	}

	if len(k.inflightJobStore) != 0 {
		t.Error("Reconcile should remove jobs from the inmemory store once it has accepted or rejected them")
	}
}

func TestReconcileJobFailed(t *testing.T) {
	//Setup... it's a long one. We need to schedule a job first
	inMemMockJobStore := []batchv1.Job{}

	create := func(b *batchv1.Job) (*batchv1.Job, error) {
		inMemMockJobStore = append(inMemMockJobStore, *b)
		return b, nil
	}

	list := func() (*batchv1.JobList, error) {
		return &batchv1.JobList{
			Items: inMemMockJobStore,
		}, nil
	}

	k, _ := NewMockKubernetesProvider(create, list)

	var rejectedMessage bool

	messageToSend := MockMessage{
		MessageID: mockMessageID,
		Rejected: func() {
			rejectedMessage = true
		},
	}

	err := k.Dispatch(messageToSend)
	if err != nil {
		t.Error(err)
	}

	job := &inMemMockJobStore[0]
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type: batchv1.JobFailed,
	})
	//Lets test things...
	err = k.Reconcile()
	if err != nil {
		t.Error(err)
	}

	if !rejectedMessage {
		t.Error("Failed to accept message during reconcilation. Expected message to be marked as accepted as job is complete")
	}

	if len(k.inflightJobStore) != 0 {
		t.Error("Reconcile should remove jobs from the inmemory store once it has accepted or rejected them")
	}
}

// AmqpMessage Wrapper for amqp
type MockMessage struct {
	MessageID          string
	DeliveryCountValue int
	Accepted           func()
	Rejected           func()
	JSONValue          string
}

// DeliveryCount get number of times the message has ben delivered
func (m MockMessage) DeliveryCount() int {
	return m.DeliveryCountValue
}

// ID get the ID
func (m MockMessage) ID() string {
	return m.MessageID
}

// Body get the body
func (m MockMessage) Body() interface{} {
	return "body"
}

// Accept mark the message as processed successfully (don't re-queue)
func (m MockMessage) Accept() error {
	m.Accepted()
	return nil
}

// Release mark the message as failed and requeue
func (m MockMessage) Reject() error {
	m.Rejected()
	return nil
}

// EventData deserialize json value to type
func (m MockMessage) EventData() (messaging.Event, error) {
	a := messaging.Event{}

	if m.JSONValue == "" {
		m.JSONValue = `{ "id": "barry", "type": "faceevnt", "parentId": "barrySnr", "correlationId": "12345" }`
	}

	err := json.Unmarshal([]byte(m.JSONValue), &a)
	if err != nil {
		log.WithError(err).WithField("value", m.JSONValue).Fatal("Unmarshal failed")
		return a, err
	}
	return a, nil
}
