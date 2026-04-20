package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPHandlers(t *testing.T) {
	mr, store := setupTestRedis()
	defer mr.Close()

	// 測試 `acquire` handler
	acquireHandler := handleAcquire(store)

	reqObj := LockRequest{
		Key:    "book:123",
		Token:  "user:uuid",
		TTLSec: 30,
	}
	body, _ := json.Marshal(reqObj)
	req, err := http.NewRequest("POST", "/lock/acquire", bytes.NewBuffer(body))
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	acquireHandler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusCreated {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusCreated)
	}

	// 測試重複發送 `acquire` 會得到 409 Conflict
	req2, _ := http.NewRequest("POST", "/lock/acquire", bytes.NewBuffer(body))
	rr2 := httptest.NewRecorder()
	acquireHandler.ServeHTTP(rr2, req2)

	if status := rr2.Code; status != http.StatusConflict {
		t.Errorf("handler return status %v, expected %v", status, http.StatusConflict)
	}

	// 測試 `extend` handler
	extendHandler := handleExtend(store)
	req3, _ := http.NewRequest("PATCH", "/lock/extend", bytes.NewBuffer(body))
	rr3 := httptest.NewRecorder()
	extendHandler.ServeHTTP(rr3, req3)

	if status := rr3.Code; status != http.StatusOK {
		t.Errorf("extend handler return status %v, expected %v", status, http.StatusOK)
	}
}
