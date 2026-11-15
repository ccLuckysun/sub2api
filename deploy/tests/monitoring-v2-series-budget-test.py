#!/usr/bin/env python3
"""Regression checks for cross-target family grouping and PG rounding evidence."""
import importlib.util
from pathlib import Path
import sys
import unittest

sys.dont_write_bytecode=True
spec=importlib.util.spec_from_file_location('budget',Path(__file__).with_name('monitoring-v2-series-budget.py'))
budget=importlib.util.module_from_spec(spec)
spec.loader.exec_module(budget)


def counts(**values):
    return {'data':{'result':[{'metric':{'__name__':name},'value':[0,str(value)]} for name,value in values.items()]}}


def metadata(**values):
    return {'data':{name:[{'type':kind} for kind in kinds.split(',')] for name,kinds in values.items()}}


class EvidenceTests(unittest.TestCase):
    def test_non_sub2api_histogram_exceeds_budget_after_merge(self):
        grouped=budget.family_counts(counts(prometheus_latency_bucket=1990,prometheus_latency_sum=6,prometheus_latency_count=6),metadata(prometheus_latency='histogram'))
        self.assertEqual(grouped,{'prometheus_latency':2002})
        self.assertGreaterEqual(max(grouped.values()),2000)

    def test_summary_quantiles_and_children_all_join(self):
        grouped=budget.family_counts(counts(go_gc_seconds=12,go_gc_seconds_sum=4,go_gc_seconds_count=4),metadata(go_gc_seconds='summary'))
        self.assertEqual(grouped,{'go_gc_seconds':20})

    def test_synthetic_and_independent_suffix_names_remain(self):
        grouped=budget.family_counts(counts(up=4,node_gauge_count=8,node_gauge=2),metadata(node_gauge='gauge',node_gauge_count='gauge'))
        self.assertEqual(grouped,{'node_gauge_count':8,'up':4,'node_gauge':2})

    def test_conflicting_target_types_fail(self):
        with self.assertRaises(budget.smoke.SmokeFailure):
            budget.family_counts(counts(shared_sum=10),metadata(shared='histogram,summary'))

    def test_parent_child_name_collision_fails(self):
        with self.assertRaises(budget.smoke.SmokeFailure):
            budget.family_counts(counts(shared_count=10),metadata(shared='histogram',shared_count='gauge'))

    def test_original_pg_row_records_two_ms_rounding_error(self):
        result=budget.phase_rounding_evidence(dict(body_read=3,gateway=4,wait=0,upstream=3010,response=0,finish=2),3021)
        self.assertEqual(result,{'sum_ms':3019,'total_ms':3021,'absolute_rounding_error_ms':2,'tolerance_ms':5})

    def test_rounding_boundary_and_rejection(self):
        self.assertEqual(budget.phase_rounding_evidence({'phase':10},15)['absolute_rounding_error_ms'],5)
        with self.assertRaises(budget.smoke.SmokeFailure):budget.phase_rounding_evidence({'phase':10},16)


if __name__=='__main__':unittest.main(verbosity=2)
