import "./style.css";
import React from "react";
import ReactDOM from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router";
import { App } from "@/App";
import { Home } from "@/routes/home";
import { Conversation } from "@/routes/conversation";
import { Connectors } from "@/routes/connectors";

const router = createBrowserRouter([
  {
    path: "/",
    Component: App,
    children: [
      { index: true, Component: Home },
      { path: "c/:id", Component: Conversation },
      { path: "settings/connectors", Component: Connectors },
    ],
  },
]);

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>,
);
