package nexus

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type successHandler struct {
	UnimplementedHandler
}

func (h *successHandler) StartOperation(ctx context.Context, request *StartOperationRequest) (OperationResponse, error) {
	if request.Operation != "i need to/be escaped" {
		return nil, newBadRequestError("unexpected operation: %s", request.Operation)
	}
	if request.CallbackURL != "http://test/callback" {
		return nil, newBadRequestError("unexpected callback URL: %s", request.CallbackURL)
	}
	if request.HTTPRequest.Header.Get("User-Agent") != userAgent {
		return nil, newBadRequestError("invalid 'User-Agent' header: %q", request.HTTPRequest.Header.Get("User-Agent"))
	}

	return &OperationResponseSync{
		Header: request.HTTPRequest.Header.Clone(),
		Body:   request.HTTPRequest.Body,
	}, nil
}

func TestSuccess(t *testing.T) {
	ctx, client, teardown := setup(t, &successHandler{})
	defer teardown()

	requestBody := []byte{0x00, 0x01}

	response, err := client.ExecuteOperation(ctx, "i need to/be escaped", requestBody, ExecuteOperationOptions{
		CallbackURL: "http://test/callback",
		Header:      http.Header{"Echo": []string{"test"}},
	})
	require.NoError(t, err)
	var responseBody []byte
	err = response.Read(&responseBody)
	require.NoError(t, err)
	// TODO: test headers
	// require.Equal(t, "test", response.Header.Get("Echo"))
	require.Equal(t, requestBody, responseBody)
}

type requestIDEchoHandler struct {
	UnimplementedHandler
}

func (h *requestIDEchoHandler) StartOperation(ctx context.Context, request *StartOperationRequest) (OperationResponse, error) {
	return &OperationResponseSync{
		Body:   bytes.NewReader([]byte(request.RequestID)),
		Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
	}, nil
}

func TestClientRequestID(t *testing.T) {
	ctx, client, teardown := setup(t, &requestIDEchoHandler{})
	defer teardown()

	type testcase struct {
		name      string
		request   StartOperationOptions
		validator func(*testing.T, []byte)
	}

	cases := []testcase{
		{
			name:    "unspecified",
			request: StartOperationOptions{},
			validator: func(t *testing.T, body []byte) {
				_, err := uuid.ParseBytes(body)
				require.NoError(t, err)
			},
		},
		{
			name:    "provided directly",
			request: StartOperationOptions{RequestID: "direct"},
			validator: func(t *testing.T, body []byte) {
				require.Equal(t, []byte("direct"), body)
			},
		},
		{
			name: "provided via headers",
			request: StartOperationOptions{
				Header: http.Header{headerRequestID: []string{"via header"}},
			},
			validator: func(t *testing.T, body []byte) {
				require.Equal(t, []byte("via header"), body)
			},
		},
		{
			name: "direct overwrites headers",
			request: StartOperationOptions{
				RequestID: "direct",
				Header:    http.Header{headerRequestID: []string{"via header"}},
			},
			validator: func(t *testing.T, body []byte) {
				require.Equal(t, []byte("direct"), body)
			},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			result, err := client.StartOperation(ctx, "foo", nil, c.request)
			require.NoError(t, err)
			response := result.Successful
			require.NotNil(t, response)
			var responseBody []byte
			err = response.Read(&responseBody)
			require.NoError(t, err)
			c.validator(t, responseBody)
		})
	}
}

type jsonHandler struct {
	UnimplementedHandler
}

func (h *jsonHandler) StartOperation(ctx context.Context, request *StartOperationRequest) (OperationResponse, error) {
	return NewOperationResponseSync("success")
}

func TestJSON(t *testing.T) {
	ctx, client, teardown := setup(t, &jsonHandler{})
	defer teardown()

	result, err := client.StartOperation(ctx, "foo", nil, StartOperationOptions{})
	require.NoError(t, err)
	response := result.Successful
	require.NotNil(t, response)
	var operationResult string
	err = response.Read(&operationResult)
	require.NoError(t, err)
	require.Equal(t, "success", operationResult)
}

type asyncHandler struct {
	UnimplementedHandler
}

func (h *asyncHandler) StartOperation(ctx context.Context, request *StartOperationRequest) (OperationResponse, error) {
	return &OperationResponseAsync{
		OperationID: "async",
	}, nil
}

func TestAsync(t *testing.T) {
	ctx, client, teardown := setup(t, &asyncHandler{})
	defer teardown()

	result, err := client.StartOperation(ctx, "foo", nil, StartOperationOptions{})
	require.NoError(t, err)
	require.NotNil(t, result.Pending)
}

type unsuccessfulHandler struct {
	UnimplementedHandler
}

func (h *unsuccessfulHandler) StartOperation(ctx context.Context, request *StartOperationRequest) (OperationResponse, error) {
	return nil, &UnsuccessfulOperationError{
		// We're passing the desired state via request ID in this test.
		State: OperationState(request.RequestID),
		Failure: Failure{
			Message: "intentional",
		},
	}
}

func TestUnsuccessful(t *testing.T) {
	ctx, client, teardown := setup(t, &unsuccessfulHandler{})
	defer teardown()

	cases := []string{"canceled", "failed"}
	for _, c := range cases {
		_, err := client.StartOperation(ctx, "foo", nil, StartOperationOptions{RequestID: c})
		var unsuccessfulError *UnsuccessfulOperationError
		require.ErrorAs(t, err, &unsuccessfulError)
		require.Equal(t, OperationState(c), unsuccessfulError.State)
	}
}
