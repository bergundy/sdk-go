package nexus

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type request struct {
	options     GetOperationResultOptions
	operation   string
	operationID string
}
type asyncWithResultHandler struct {
	UnimplementedHandler
	timesToBlock int
	resultError  error
	requests     []request
}

func (h *asyncWithResultHandler) StartOperation(ctx context.Context, operation string, input *EncodedStream, options StartOperationOptions) (OperationResponse[any], error) {
	return &OperationResponseAsync{
		OperationID: "a/sync",
	}, nil
}

func (h *asyncWithResultHandler) getResult() (any, error) {
	if h.resultError != nil {
		return nil, h.resultError
	}
	return []byte("body"), nil
}

func (h *asyncWithResultHandler) GetOperationResult(ctx context.Context, operation, operationID string, options GetOperationResultOptions) (any, error) {
	h.requests = append(h.requests, request{options: options, operation: operation, operationID: operationID})

	if options.Header.Get("User-Agent") != userAgent {
		return nil, newBadRequestError("invalid 'User-Agent' header: %q", options.Header.Get("User-Agent"))
	}
	if options.Header.Get("Content-Type") != "" {
		return nil, newBadRequestError("'Content-Type' header set on request")
	}
	if options.Wait == 0 {
		return h.getResult()
	}
	if options.Wait > 0 {
		deadline, set := ctx.Deadline()
		if !set {
			return nil, newBadRequestError("context deadline unset")
		}
		timeout := time.Until(deadline)
		diff := (getResultMaxTimeout - timeout).Abs()
		if diff > time.Millisecond*100 {
			return nil, newBadRequestError("context deadline invalid, timeout: %v", timeout)
		}
	}
	if len(h.requests) <= h.timesToBlock {
		ctx, cancel := context.WithTimeout(ctx, options.Wait)
		defer cancel()
		<-ctx.Done()
		return nil, ErrOperationStillRunning
	}
	return h.getResult()
}

func TestWaitResult(t *testing.T) {
	handler := asyncWithResultHandler{timesToBlock: 1}
	ctx, client, teardown := setup(t, &handler)
	defer teardown()

	response, err := client.ExecuteOperation(ctx, "f/o/o", nil, ExecuteOperationOptions{
		Header: http.Header{
			"foo": []string{"bar"},
		},
	})
	require.NoError(t, err)
	var body []byte
	err = response.Read(&body)
	require.NoError(t, err)
	require.Equal(t, []byte("body"), body)

	require.Equal(t, 2, len(handler.requests))
	require.InDelta(t, testTimeout+getResultContextPadding, handler.requests[0].options.Wait, float64(time.Millisecond*50))
	require.InDelta(t, testTimeout+getResultContextPadding-getResultMaxTimeout, handler.requests[1].options.Wait, float64(time.Millisecond*50))
	require.Equal(t, "f/o/o", handler.requests[0].operation)
	require.Equal(t, "a/sync", handler.requests[0].operationID)
}

func TestWaitResult_StillRunning(t *testing.T) {
	ctx, client, teardown := setup(t, &asyncWithResultHandler{timesToBlock: 1000})
	defer teardown()

	result, err := client.StartOperation(ctx, "foo", nil, StartOperationOptions{})
	require.NoError(t, err)
	handle := result.Pending
	require.NotNil(t, handle)

	ctx = context.Background()
	_, err = handle.GetResult(ctx, GetOperationResultOptions{Wait: time.Millisecond * 200})
	require.ErrorIs(t, err, ErrOperationStillRunning)
}

func TestWaitResult_DeadlineExceeded(t *testing.T) {
	ctx, client, teardown := setup(t, &asyncWithResultHandler{timesToBlock: 1000})
	defer teardown()

	result, err := client.StartOperation(ctx, "foo", nil, StartOperationOptions{})
	require.NoError(t, err)
	handle := result.Pending
	require.NotNil(t, handle)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*200)
	defer cancel()
	_, err = handle.GetResult(ctx, GetOperationResultOptions{Wait: time.Second})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestPeekResult_StillRunning(t *testing.T) {
	handler := asyncWithResultHandler{resultError: ErrOperationStillRunning}
	ctx, client, teardown := setup(t, &handler)
	defer teardown()

	handle, err := client.NewHandle("foo", "a/sync")
	require.NoError(t, err)
	response, err := handle.GetResult(ctx, GetOperationResultOptions{})
	require.ErrorIs(t, err, ErrOperationStillRunning)
	require.Nil(t, response)
	require.Equal(t, 1, len(handler.requests))
	require.Equal(t, time.Duration(0), handler.requests[0].options.Wait)
}

func TestPeekResult_Success(t *testing.T) {
	ctx, client, teardown := setup(t, &asyncWithResultHandler{})
	defer teardown()

	handle, err := client.NewHandle("foo", "a/sync")
	require.NoError(t, err)
	response, err := handle.GetResult(ctx, GetOperationResultOptions{})
	require.NoError(t, err)
	var body []byte
	err = response.Read(&body)
	require.NoError(t, err)
	require.Equal(t, []byte("body"), body)
}

func TestPeekResult_Canceled(t *testing.T) {
	ctx, client, teardown := setup(t, &asyncWithResultHandler{resultError: &UnsuccessfulOperationError{State: OperationStateCanceled}})
	defer teardown()

	handle, err := client.NewHandle("foo", "a/sync")
	require.NoError(t, err)
	_, err = handle.GetResult(ctx, GetOperationResultOptions{})
	var unsuccessfulOperationError *UnsuccessfulOperationError
	require.ErrorAs(t, err, &unsuccessfulOperationError)
	require.Equal(t, OperationStateCanceled, unsuccessfulOperationError.State)
}
