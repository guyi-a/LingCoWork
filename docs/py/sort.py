from heapq import heapify
from operator import le
from sys import last_exc


def bubbleSort(arr):
    n = len(arr)
    for i in range(n - 1, 0, -1):
        for j in range(i):
            if arr[i] > arr[i + 1]:
                arr[i], arr[i + 1] = arr[i + 1], arr[i]

def quickSort(arr, left, right):
    if left >= right:
        return 
    pivot = arr[left]
    i, j = left, right
    while i < j:
        while i < j and arr[j] >= pivot:
            j -= 1
        while i < j and arr[i] <= pivot:
            i += 1
        if i < j:
            arr[i], arr[j] = arr[j], arr[i]
    arr[left], arr[i] = arr[i], arr[left]
    quickSort(arr, left, i - 1)
    quickSort(arr, i + 1, right)

def mergeSort(arr, left, right):
    if left > right:
        return 
    mid = left + (right - left) // 2
    mergeSort(arr, left, mid)
    mergeSort(arr, mid + 1, right)
    merge(arr, left, mid, right)

def merge(arr, left, mid, right):
    res = []
    i, j = left, mid + 1
    while i <= mid and j <= right:
        if arr[i] <= arr[j]:
            res.append(arr[i])
            i += 1
        else:
            res.append(arr[j])
            j += 1
    while i <= mid:
        res.append(arr[i])
        i += 1
    while j <= right:
        res.append(arr[j])
        j += 1
    arr[left: right + 1] = res

def heapSort(heap):
    n = len(heap)
    for i in range(n // 2 - 1, -1, -1):
        heapify(heap, i, n)
    for i in range(n - 1, 0, -1):
        heap[0], heap[i] = heap[i], heap[0]
        heapify(heap, 0, i)
    
def heapify(heap, i, n):
    largest = i
    left, right = 2 * i + 1, 2 * i + 2
    if left < n and heap[left] > heap[largest]:
        largest = left
    if right < n and heap[right] > heap[largest]:
        largest = right
    if largest != i:
        heap[largest], heap[i] = heap[i], heap[largest]
        heapify(heap, largest, n)



