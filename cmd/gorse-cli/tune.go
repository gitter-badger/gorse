// Copyright 2020 gorse Project Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"fmt"
	"github.com/olekukonko/tablewriter"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/zhenghaoz/gorse/model/cf"
	"github.com/zhenghaoz/gorse/model/rank"
	"github.com/zhenghaoz/gorse/storage/data"
	"os"
	"runtime"
	"time"
)

func init() {
	cliCommand.AddCommand(tuneCommand)
	// test match model
	tuneCommand.AddCommand(tuneMatchCommand)
	tuneMatchCommand.PersistentFlags().String("feedback-type", "", "Set feedback type.")
	tuneMatchCommand.PersistentFlags().String("load-builtin", "", "load data from built-in")
	tuneMatchCommand.PersistentFlags().String("load-csv", "", "load data from CSV file")
	tuneMatchCommand.PersistentFlags().String("load-database", "", "load data from database")
	tuneMatchCommand.PersistentFlags().String("csv-sep", "\t", "load CSV file with separator")
	tuneMatchCommand.PersistentFlags().String("csv-format", "", "load CSV file with header")
	tuneMatchCommand.PersistentFlags().Bool("csv-header", false, "load CSV file with header")
	tuneMatchCommand.PersistentFlags().Int("verbose", 1, "Verbose period")
	tuneMatchCommand.PersistentFlags().Int("jobs", runtime.NumCPU(), "Number of jobs for model fitting")
	tuneMatchCommand.PersistentFlags().Int("top-k", 10, "Length of recommendation list")
	tuneMatchCommand.PersistentFlags().Int("n-negatives", 100, "Number of users for sampled test set")
	tuneMatchCommand.PersistentFlags().Int("n-test-users", 0, "Number of users for sampled test set")
	tuneMatchCommand.PersistentFlags().IntP("n-trials", "t", 10, "Number of trials")
	for _, paramFlag := range matchParamFlags {
		tuneMatchCommand.PersistentFlags().String(paramFlag.Name, "", paramFlag.Help)
	}
	// test rank model
	tuneCommand.AddCommand(tuneRankCommand)
	tuneRankCommand.PersistentFlags().String("load-builtin", "", "load data from built-in")
	tuneRankCommand.PersistentFlags().String("load-database", "", "load data from database")
	tuneRankCommand.PersistentFlags().Float32("test-ratio", 0.2, "Test ratio.")
	tuneRankCommand.PersistentFlags().String("task", "r", "Task for ranking (c - classification, r - regression)")
	tuneRankCommand.PersistentFlags().Int("verbose", 1, "Verbose period")
	tuneRankCommand.PersistentFlags().Int("jobs", runtime.NumCPU(), "Number of jobs for model fitting")
	tuneRankCommand.PersistentFlags().IntP("n-trials", "t", 10, "Number of trials")
	for _, paramFlag := range rankParamFlags {
		tuneRankCommand.PersistentFlags().String(paramFlag.Name, "", paramFlag.Help)
	}
}

var tuneCommand = &cobra.Command{
	Use:   "tune",
	Short: "tune recommendation model by random search",
}

var tuneMatchCommand = &cobra.Command{
	Use:   "match",
	Short: "Tune match model by random search",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		modelName := args[0]
		m, err := cf.NewModel(modelName, nil)
		if err != nil {
			log.Fatal(err)
		}
		// Load data
		var trainSet, testSet *cf.DataSet
		if cmd.PersistentFlags().Changed("load-builtin") {
			name, _ := cmd.PersistentFlags().GetString("load-builtin")
			trainSet, testSet, err = cf.LoadDataFromBuiltIn(name)
			if err != nil {
				log.Fatal("failed to load built-in dataset:", err)
			}
			log.Printf("Load built-in dataset %s\n", name)
		} else if cmd.PersistentFlags().Changed("load-csv") {
			name, _ := cmd.PersistentFlags().GetString("load-csv")
			sep, _ := cmd.PersistentFlags().GetString("csv-sep")
			header, _ := cmd.PersistentFlags().GetBool("csv-header")
			numTestUsers, _ := cmd.PersistentFlags().GetInt("n-test-users")
			seed, _ := cmd.PersistentFlags().GetInt("random-state")
			data := cf.LoadDataFromCSV(name, sep, header)
			trainSet, testSet = data.Split(numTestUsers, int64(seed))
		} else {
			log.Println("Load dataset from database")
			feedbackType, _ := cmd.PersistentFlags().GetString("feedback-type")
			numTestUsers, _ := cmd.PersistentFlags().GetInt("n-test-users")
			seed, _ := cmd.PersistentFlags().GetInt("random-state")
			// Open database
			database, err := data.Open(globalConfig.Database.DataStore)
			if err != nil {
				log.Fatalf("cli: failed to connect database (%v)", err)
			}
			defer database.Close()
			// Load data
			data, _, err := cf.LoadDataFromDatabase(database, []string{feedbackType})
			if err != nil {
				log.Fatalf("cli: failed to load data from database (%v)", err)
			}
			if data.Count() == 0 {
				log.Fatalf("cli: empty dataset")
			}
			log.Infof("data set: #user = %v, #item = %v, #feedback = %v", data.UserCount(), data.ItemCount(), data.Count())
			trainSet, testSet = data.Split(numTestUsers, int64(seed))
		}
		log.Infof("train set: #user = %v, #item = %v, #feedback = %v", trainSet.UserCount(), trainSet.ItemCount(), trainSet.Count())
		log.Infof("test set: #user = %v, #item = %v, #feedback = %v", testSet.UserCount(), testSet.ItemCount(), testSet.Count())
		// Load hyper-parameters
		grid := parseParamFlags(cmd)
		log.Printf("Load hyper-parameters grid: %v\n", grid)
		// Load runtime options
		fitConfig := &cf.FitConfig{}
		fitConfig.Verbose, _ = cmd.PersistentFlags().GetInt("verbose")
		fitConfig.Jobs, _ = cmd.PersistentFlags().GetInt("jobs")
		fitConfig.TopK, _ = cmd.PersistentFlags().GetInt("top-k")
		fitConfig.Candidates, _ = cmd.PersistentFlags().GetInt("n-negatives")
		// Cross validation
		start := time.Now()
		grid.Fill(m.GetParamsGrid())
		log.Printf("Tune hyper-parameters on: %v\n", grid)
		numTrials, _ := cmd.PersistentFlags().GetInt("n-trials")
		result := cf.RandomSearchCV(m, trainSet, testSet, grid, numTrials, 0, fitConfig)
		elapsed := time.Since(start)
		// Render table
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"#", "NDCG@10", "Precision@10", "Recall@10", "Params"})
		for i := range result.Params {
			score := result.Scores[i]
			table.Append([]string{
				fmt.Sprintf("%v", i),
				fmt.Sprintf("%v", score.NDCG),
				fmt.Sprintf("%v", score.Precision),
				fmt.Sprintf("%v", score.Recall),
				fmt.Sprintf("%v", result.Params[i]),
			})
		}
		table.Render()
		log.Printf("Complete cross validation (%v)\n", elapsed)
	},
}

var tuneRankCommand = &cobra.Command{
	Use:   "rank",
	Short: "Tune rank model by random search.",
	Run: func(cmd *cobra.Command, args []string) {
		var err error
		// Load data
		var trainSet, testSet *rank.Dataset
		if cmd.PersistentFlags().Changed("load-builtin") {
			name, _ := cmd.PersistentFlags().GetString("load-builtin")
			log.Infof("Load built-in dataset %s\n", name)
			trainSet, testSet, err = rank.LoadDataFromBuiltIn(name)
			if err != nil {
				log.Fatal("cli: ", err)
			}
		} else {
			// load dataset
			feedbackType, _ := cmd.PersistentFlags().GetString("feedback-type")
			// Open database
			database, err := data.Open(globalConfig.Database.DataStore)
			if err != nil {
				log.Fatalf("cli: failed to connect database (%v)", err)
			}
			defer database.Close()
			seed, _ := cmd.PersistentFlags().GetInt64("seed")
			testRatio, _ := cmd.PersistentFlags().GetFloat32("test-ratio")
			log.Infof("Load data from database")
			dataSet, err := rank.LoadDataFromDatabase(database, []string{feedbackType})
			if err != nil {
				log.Fatalf("cli: failed to load data from database (%v)", err)
			}
			if dataSet.PositiveCount == 0 {
				log.Fatalf("cli: empty dataset")
			}
			log.Infof("data set: #user = %v, #item = %v, #positive = %v",
				dataSet.UserCount(), dataSet.ItemCount(), dataSet.PositiveCount)
			trainSet, testSet = dataSet.Split(testRatio, seed)
			testSet.NegativeSample(1, trainSet, 0)
		}
		log.Infof("train set: #user = %v, #item = %v, #positive = %v", trainSet.UserCount(), trainSet.ItemCount(), trainSet.PositiveCount)
		log.Infof("test set: #user = %v, #item = %v, #positive = %v", testSet.UserCount(), testSet.ItemCount(), testSet.PositiveCount)
		// Load hyper-parameters
		grid := parseParamFlags(cmd)
		log.Printf("Load hyper-parameters grid: %v\n", grid)
		// Load runtime options
		fitConfig := &rank.FitConfig{}
		fitConfig.Verbose, _ = cmd.PersistentFlags().GetInt("verbose")
		fitConfig.Jobs, _ = cmd.PersistentFlags().GetInt("jobs")
		// Cross validation
		task, _ := cmd.PersistentFlags().GetString("task")
		m := rank.NewFM(rank.FMTask(task), nil)
		start := time.Now()
		grid.Fill(m.GetParamsGrid())
		log.Printf("Tune hyper-parameters on: %v\n", grid)
		numTrials, _ := cmd.PersistentFlags().GetInt("n-trials")
		result := rank.RandomSearchCV(m, trainSet, testSet, grid, numTrials, 0, fitConfig)
		elapsed := time.Since(start)
		// Render table
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"#", result.BestScore.GetName(), "Params"})
		for i := range result.Params {
			score := result.Scores[i]
			table.Append([]string{
				fmt.Sprintf("%v", i),
				fmt.Sprintf("%v", score.GetValue()),
				fmt.Sprintf("%v", result.Params[i].ToString()),
			})
		}
		table.Render()
		log.Printf("Complete cross validation (%v)\n", elapsed)
	},
}
