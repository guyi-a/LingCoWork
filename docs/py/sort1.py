from calendar import JULY
from curses import qiflush
from heapq import heapify
from math import pi
from operator import le
import re


# Quick sort
# Time: best/average O(n log n), worst O(n^2) when pivot is always max/min.
# Space: average O(log n) recursion stack, worst O(n).
# Stable: no.
def quickSort(arr, left, right):
    if left >= right:
        return
    i, j = left, right
    pivot = arr[left]
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

# Merge sort
# Time: best/average/worst O(n log n).
# Space: O(n) for temporary merge arrays.
# Stable: yes, because equal elements are taken from the left side first.
def mergeSort(arr, left, right):
    if left >= right:
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


# Heap sort
# Time: best/average/worst O(n log n). Build heap is O(n), then n heapify calls.
# Space: O(1) extra space.
# Stable: no.
def heapSort(arr):
    n = len(arr)
    for i in range(n // 2 - 1, -1, -1):
        heapify(arr, i, n)
    for i in range(n - 1, 0, -1):
        arr[0], arr[i] = arr[i], arr[0]
        heapify(arr, 0, i)
    
def heapify(arr, i, n):
    largest = i
    left, right = 2 * i + 1, 2 * i + 2
    if left < n and arr[left] > arr[largest]:
        largest = left
    if right < n and arr[right] > arr[largest]:
        largest = right
    if largest != i:
        arr[largest], arr[i] = arr[i], arr[largest]
        heapify(arr, largest, n)

# Bubble sort
# Time: best/average/worst O(n^2).
# Space: O(1) extra space.
# Stable: yes, because equal elements are not swapped.
def bubbleSort(arr):
    n = len(arr)
    for j in range(n - 1, 0, -1):
        for i in range(j):
            if arr[i] > arr[i + 1]:
                arr[i], arr[i + 1] = arr[i + 1], arr[i]


